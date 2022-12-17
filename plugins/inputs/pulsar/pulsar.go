package pulsar

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/srgsf/pulsar-telegraf-plugin/log"
	pulsar "github.com/srgsf/tvh-pulsar"
)

const maxChanId = 16
const maxRetries = 3
const (
	keyMeterId   = "id"
	keyError     = "error_key"
	keyErrDesc   = "error_description"
	keyNetStatus = "net_status"
	keyTimeDiff  = "time_diff"
	measurment   = "pulsar"
)

var version = "dev"

// pulsar,id net_status,error_cd,error_desc,time_diff,chan_X time
type device struct {
	Socket          string
	Address         string
	Status          Duration      `toml:"status_interval"`
	Timzone         string        `toml:"systime_tz"`
	LogProto        bool          `toml:"log_protocol"`
	Pass            []uint        `toml:"channels_include"`
	Prefix          string        `toml:"channel_prefix"`
	LogLevel        *log.LogLevel `toml:"log_level"`
	cl              client
	location        *time.Location
	lastStatusTime  time.Time
	prevIsConnected bool
}

type client struct {
	dialer      pulsar.Dialer
	conn        pulsar.Conn
	handle      *pulsar.Client
	isConnected bool
}

type result struct {
	tags   map[string]string
	fields map[string]any
}

func (d *device) Init() error {
	logLevel := log.LvlInfo
	if d.LogLevel != nil {
		logLevel = *d.LogLevel
	}

	log.InitLoggers(os.Stderr, logLevel)

	log.Infof("pulsar plugin version: %s", version)
	log.Debug("plugin initialization started")
	if d.Socket == "" {
		log.Error("socket configuration property is not set")
		return errors.New("socket is required")
	}

	if d.Address == "" {
		log.Error("address configuration property is not set")
		return errors.New("address is required")
	}

	d.location = time.UTC
	if d.Timzone != "" {
		location, err := time.LoadLocation(d.Timzone)
		if err != nil {
			log.Error("invalid systime_tz value")
			return errors.New("invalid timezone")
		}
		d.location = location
	}

	if d.LogProto {
		log.Debug("protocol logger enabled")
		d.cl.dialer.ProtocolLogger = log.DEBUG
	}

	d.cl.dialer.ConnectionTimeOut = 20 * time.Second
	d.cl.dialer.RWTimeOut = 10 * time.Second
	chs, err := newChannels(d.Pass)
	if err != nil {
		log.Error("unable to configure channels")
		return err
	}
	d.Pass = chs
	log.Debug("plugin initialization finished")
	return nil
}

func newChannels(chs []uint) ([]uint, error) {
	if len(chs) == 0 {
		log.Error("channels_include configuration property is not set")
		return nil, errors.New("channels_include is required")
	}
	set := make(map[uint]any)
	for _, v := range chs {
		if 0 >= v || v > maxChanId {
			return nil, fmt.Errorf("invalid cannel %d", v)
		}
		set[v] = true
	}
	chs = chs[:0]
	for k := range set {
		chs = append(chs, uint(k))
	}
	sort.Slice(chs, func(i, j int) bool { return chs[i] < chs[j] })
	return chs, nil
}

func (d *device) SampleConfig() string {
	return `
## Gather data from Pulsar-M pulse registrator ##
[[inputs.pulsar]]
    ## tcp socket address for rs485 to ethernet converter.
    socket ="localhost:4001"
    ## device address.
    address = "00112233"
    ## Status request interval - don't request if ommited or 0
    status_interval = "1d"
    ## Timezone of device system time.
    systime_tz = "Europe/Moscow"
    ## should protocol be logged as debug output.
    # log_protocol = true
    ## log level. Possible values are error,warning,info,debug
    #log_level = "info"
    ## query only the following channels starts with 1 for summary.
    channels_include = [1,2]
    ## value prefix for a channel
    chanel_prefix = "chan_"
`
}

func (d *device) Description() string {
	return "Reads Pulsar-M pulse registrator data via tcp"
}

func (d *device) Gather(acc telegraf.Accumulator) error {
	t, err := d.gatherData(acc)
	if d.prevIsConnected != d.cl.isConnected {
		status := "offline"
		if d.cl.isConnected {
			status = "online"
		}
		acc.AddFields(measurment, map[string]any{keyNetStatus: status},
			map[string]string{keyMeterId: d.Address}, t)
		d.prevIsConnected = d.cl.isConnected
	}
	if err == nil {
		acc.AddFields(measurment,
			map[string]any{keyTimeDiff: int64(time.Until(t) / time.Second)},
			map[string]string{keyMeterId: d.Address}, t)
	}
	return err
}

func (d *device) gatherData(acc telegraf.Accumulator) (time.Time, error) {
	withRetries := func(fn func() error) error {
		var err error
		for i := 0; i < maxRetries; i++ {
			if err = fn(); err == nil {
				break
			}
		}
		return err
	}

	var t time.Time
	err := withRetries(func() error {
		var err error
		t, err = d.systime()
		return err
	})

	if err != nil {
		log.Warnf("pulsar systime gather error: %s", err.Error())
		return time.Now(), err
	}

	err = withRetries(func() error {
		stat, err := d.gatherStatus()
		if err == nil {
			for _, v := range stat {
				acc.AddFields(measurment, v.fields, v.tags, t)
			}
		}
		return err
	})

	if err != nil {
		log.Warnf("pulsar status gather error: %s", err.Error())
		return t, err
	}

	err = withRetries(func() error {
		v, err := d.gatherValues()
		if err == nil {
			acc.AddFields(measurment, v.fields, v.tags, t)
		}
		return err
	})

	if err != nil {
		log.Warnf("pulsar values gather error: %s", err.Error())
	}
	return t, err
}

func (d *device) gatherStatus() ([]result, error) {
	if d.Status.Empty() || d.Status.Until(d.lastStatusTime) >= 0 {
		return nil, nil
	}

	stat, err := d.curState()
	if err != nil {
		return nil, err
	}
	var rv []result

	for k, v := range stat {
		rv = append(rv, result{
			tags:   map[string]string{keyMeterId: d.Address},
			fields: map[string]any{keyError: k, keyErrDesc: v},
		})
	}
	d.lastStatusTime = time.Now()
	return rv, nil
}

func (d *device) gatherValues() (*result, error) {
	log.Debug("current values request started")
	c, err := d.client()
	if err != nil {
		return nil, err
	}

	ch, err := c.CurValues(d.Pass...)
	if err != nil {
		log.Warn("current values request failed")
		d.cl.isConnected = false
		return nil, err
	}
	log.Debug("current values request succeed")
	rv := &result{
		tags:   map[string]string{keyMeterId: d.Address},
		fields: map[string]any{},
	}
	for i, c := range ch {
		rv.fields[fmt.Sprintf("%s%d", d.Prefix, d.Pass[i])] = c.Value
	}
	return rv, nil
}

func (d *device) client() (*pulsar.Client, error) {
	if d.cl.isConnected {
		return d.cl.handle, nil
	}
	if d.cl.conn != nil {
		_ = d.cl.conn.Close()
		d.cl.conn = nil
	}
	log.Debug("connection to pulse registrator started")
	conn, err := d.cl.dialer.DialTCP(d.Socket)
	if err != nil {
		log.Warn("connection to pulse registrator failed")
		return nil, err
	}
	log.Debug("connection to pulse registrator succeed")
	if d.cl.handle == nil {
		log.Debug("client setup started")
		client, err := pulsar.NewClient(d.Address, conn)
		if err != nil {
			log.Warnf("unable to init pulsar client with address: %s\n%s\n",
				d.Address, err.Error())
			_ = conn.Close()
			return nil, err
		}
		d.cl.handle = client
		log.Debug("client setup succeed")
	}
	d.cl.handle.Reset(conn)
	d.cl.conn = conn
	d.cl.isConnected = true
	return d.cl.handle, nil
}

func (d *device) systime() (time.Time, error) {
	log.Debug("device systime request started")
	c, err := d.client()
	if err != nil {
		return time.Time{}, err
	}

	t, err := c.SysTime()
	if err != nil {
		d.cl.isConnected = false
		log.Warn("device systime request failed")
		return time.Time{}, err
	}
	log.Debug("device systime request succeed")
	return time.Date(t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), d.location), nil
}

func (d *device) curState() (map[string]string, error) {
	log.Debug("device diagnostic request started")
	c, err := d.client()
	if err != nil {
		return nil, err
	}
	v, err := c.DiagnosticsFlags()
	if err != nil {
		log.Warn("device diagnostic request failed")
		d.cl.isConnected = false
		return nil, err
	}
	log.Debug("device diagnostic request succeed")

	rv := make(map[string]string)
	if v&0x04 != 0 {
		rv["EEPROM"] = "EEPROM write fail"
	}
	if v&0x08 != 0 {
		rv["CHAN_ERR"] = "A Channel has a negative value"
	}
	return rv, nil
}

func init() {
	inputs.Add("pulsar", func() telegraf.Input {
		return &device{}
	})
}

type Duration struct {
	tt    time.Duration
	monts int
	years int
}

// UnmarshalTOML parses the duration from the TOML config file
func (d *Duration) UnmarshalTOML(b []byte) error {
	var cd config.Duration
	err := cd.UnmarshalTOML(b)
	if err == nil {
		*d = Duration{tt: time.Duration(cd)}
		return nil
	}

	isDigit := func(ch rune) bool { return ('0' <= ch && ch <= '9') }
	newErr := func() error {
		return fmt.Errorf("invalid durtion: %s", string(b))
	}

	dR := []rune(string(b))
	*d = Duration{}
	var i int
	for i < len(dR) {
		s := i
		for ; i < len(dR) && isDigit(dR[i]); i++ {
			//digits
		}
		if i >= len(dR) || i == s {
			return newErr()
		}
		n, err := strconv.ParseInt(string(dR[s:i]), 10, 64)
		if err != nil {
			return newErr()
		}
		switch dR[i] {
		case 's':
			d.tt += time.Duration(n) * time.Second
		case 'h':
			d.tt += time.Duration(n) * time.Hour
		case 'd':
			d.tt += time.Duration(n) * 24 * time.Hour
		case 'w':
			d.tt += time.Duration(n) * 7 * 24 * time.Hour
		case 'y':
			d.years = int(n)
		case 'n':
			d.tt += time.Duration(n) * time.Nanosecond
			if i+1 < len(dR) && dR[i+1] == 's' {
				i += 2
				continue
			}
		case 'u', 'µ':
			d.tt += time.Duration(n) * time.Microsecond
			if i+1 < len(dR) && dR[i+1] == 's' {
				i += 2
				continue
			}
		case 'm':
			if i+1 < len(dR) && dR[i+1] == 's' {
				d.tt += time.Duration(n) * time.Millisecond
				i += 2
				continue
			}

			if i+1 < len(dR) && dR[i+1] == 'o' {
				d.monts = int(n)
				i += 2
				continue
			}
			d.tt += time.Duration(n) * time.Minute
		default:
			return newErr()
		}
		i++
	}
	return nil
}

func (d *Duration) UnmarshalText(text []byte) error {
	return d.UnmarshalTOML(text)
}

func (d *Duration) Empty() bool {
	return d.tt == 0 && d.years == 0 && d.monts == 0
}

func (d *Duration) Until(t time.Time) time.Duration {
	t = t.AddDate(d.years, d.monts, 0)
	t = t.Add(d.tt)
	return time.Until(t)
}
