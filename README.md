# Telegraf input plugin for Pulsar-M pulse registrators.
[![Coverage Status](https://coveralls.io/repos/github/srgsf/pulsar-telegraf-plugin/badge.svg)](https://coveralls.io/github/srgsf/pulsar-telegraf-plugin)
[![lint and test](https://github.com/srgsf/pulsar-telegraf-plugin/actions/workflows/golint-ci.yaml/badge.svg)](https://github.com/srgsf/pulsar-telegraf-plugin/actions/workflows/golint-ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/srgsf/pulsar-telegraf-plugin)](https://goreportcard.com/report/github.com/srgsf/pulsar-telegraf-plugin)

This is a [Pulsar-M](https://pulsarm.com/en/products/converters-pulsar/pulse-registrator-pulsar/) pulse registrator input plugin for Telegraf, meant to be compiled separately and used externally with telegraf's execd input plugin.

It reads channel current values and health status from the registrator using [Pulsar-M](https://github.com/srgsf/tvh-pulsar) protocol wrapper via tcp.

## Install Instructions

Download [release](https://github.com/srgsf/pulsar-telegraf-plugin/releases) for your target architectrue.

Extract archieve and edit plugin.conf file.

You should be able to call this from telegraf now using execd:

```toml
[[inputs.execd]]
  command = ["/path/to/pulsar", "-config", "plugin.conf", "-poll_interval", "1m"]
  signal = "none"

# sample output: write metrics to stdout
[[outputs.file]]
  files = ["stdout"]
```

## Build from sources

Download the repo somewhere

    $ git clone https://github.com/srgsf/pulsar-telegraf-plugin.git

Build the binary for your platform using make

    $ make build

The binary will be avalilable at ./dist/pulsar

## Plugin configuration example

```toml
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
```
