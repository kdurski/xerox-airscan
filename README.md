# Xerox AirScan bridge

Use a Xerox WorkCentre 3225 network scanner with Apple Image Capture, Preview,
and other applications using Apple's built-in AirScan driver. The bridge talks
to the printer on TCP port 9400 and advertises an AirScan/eSCL service over Bonjour.

Supports flatbed scans, simplex document-feeder batches, color, grayscale, and
custom scan regions. Image Capture handles PDF creation and combining pages.
No Xerox driver, SANE installation, Docker, or firmware modification is required.

## Quick start

Requires macOS and Go 1.25 or later to build. The executable has no external Go
modules or runtime dependencies beyond macOS's built-in `dns-sd`.

```sh
make build
cp config.example.json config.json
```

Edit `config.json` and replace the example scanner address with your printer's
IP address or hostname. The smallest configuration is:

```json
{
  "scanner_address": "192.0.2.10:9400"
}
```

`192.0.2.10` is a documentation placeholder. Use your actual printer address.
The port defaults to 9400 when omitted. Then start the bridge:

```sh
./bin/xerox-airscan serve
```

Leave it running and open **Image Capture → Xerox WorkCentre 3225 AirScan**.
Select **Flatbed** or **Document Feeder** under **Scan Mode**. For a single PDF
from a feeder batch, select **PDF → Combine into single document**.

You can also double-click `Start Scanner.command` after creating the configuration.
It runs from the project directory and builds the executable if it is missing.
Stop the bridge with Control-C. Rebuild and restart after source or config changes.

## Run automatically in the background on macOS

Quit any manually running bridge with Control-C, then run:

```sh
make service-install
```

This builds with asdf and installs a LaunchAgent that starts immediately and at
each login, without a Terminal window. macOS restarts it if it exits. At startup
it waits and retries every 30 seconds until the printer is reachable. It runs
while your account is logged in; scanning requires the Mac to be awake.

The service uses a locally signed `Xerox AirScan.app` bundle so macOS can identify
its Local Network request. Allow **Xerox AirScan** to access your local network
when prompted, or enable it in **System Settings → Privacy & Security → Local
Network**. The executable and configuration are copied to
`~/Library/Application Support/Xerox AirScan/`, so moving this checkout or closing
Terminal does not stop the service. No Go tools or asdf are needed at runtime.

Edit the installed `config.json` to change the service's settings, then run
`make service-restart`. Re-run `make service-install` after updating the source;
it preserves your installed configuration.

```sh
make service-status     # launchd state, PID, and last exit status
make service-restart    # reload after changing installed configuration
make service-stop       # stop and disable automatic startup
make service-start      # re-enable automatic startup and start now
make service-uninstall  # remove login service; keep configuration and logs
```

The launchd definition is `~/Library/LaunchAgents/local.xerox-airscan.plist`.
Logs are in `~/Library/Logs/Xerox AirScan/stdout.log` and `stderr.log`.
Use the service instead of also starting `Start Scanner.command`.

## Configuration

The bridge reads `config.json` from the current working directory. Your local
configuration is ignored by Git; `config.example.json` is the shareable template.

| Key | Default | Meaning |
|---|---|---|
| `scanner_address` | Required | Printer IP/hostname, optionally including a port |
| `listen_address` | `0.0.0.0:8090` | HTTP listening address |
| `name` | `Xerox WorkCentre 3225 AirScan` | Bonjour display name |
| `bonjour` | `true` | Publish the scanner over Bonjour |
| `feeder` | `true` | Advertise the simplex document feeder |
| `verbose` | `false` | Log requests, settings, and protocol status replies |

Command-line options override file settings, including explicit `false` values:

```sh
./bin/xerox-airscan serve --config /path/to/scanner.json
./bin/xerox-airscan doctor --printer scanner.local
./bin/xerox-airscan serve --adf=false --verbose
```

An explicitly selected config file must exist. A missing default `config.json`
is allowed only when `--printer` supplies the scanner address. Invalid settings
and unknown JSON keys fail with an error rather than silently using a default.
Run `xerox-airscan COMMAND --help` for the available options.

## Direct scanning

Query the printer's identity and reported capabilities:

```sh
./bin/xerox-airscan doctor
```

Scan to image files without Image Capture:

```sh
mkdir -p out
./bin/xerox-airscan scan --dpi 300 --output out/document.png
./bin/xerox-airscan scan --dpi 150 --gray --output out/grayscale.jpg
./bin/xerox-airscan scan --source Feeder --dpi 300 --output out/page.png
```

The feeder command scans the entire batch. The first page uses the requested
filename; subsequent pages use `page-002.png`, `page-003.png`, and so on.
Files are created with private permissions; existing files are never overwritten.

The printer reports resolutions of 75, 100, 150, 200, 300, and 600 dpi. The bridge
checks each request against the live device capabilities. JPEG strips are acquired
in color; grayscale conversion and precise cropping happen in the bridge. PNG
avoids another JPEG compression pass, but the scanner's source data is already JPEG.

## Feeder behavior and limits

One Xerox connection and reservation is kept for the full batch. Each page is
advanced explicitly, and an empty feeder ends the batch. Shorter sheets retain
their detected image height. Jam, cover-open, and incomplete-transfer errors do
not count as successful completion. An abandoned job expires after five minutes;
this idle timeout resets after each page.

Only the WorkCentre 3225 has been validated. The feeder is simplex; duplex
printing does not imply duplex scanning. Scan settings remain fixed within a
batch. The live flatbed area is approximately 216 × 297 mm, with feeder lengths
up to 356 mm. A three-sheet, 300 dpi batch and flatbed overview/full scans have
been verified through Apple's actual AirScan driver.

## Development and tests

```sh
make test
make check
```

If Go is managed by asdf, use `make GO='asdf exec go' build test check`.

The default tests require no printer, configuration, network access, or private
captures. They generate synthetic images and TCP traffic to test packet
reassembly, fragmented reads, page advance, cropping, cancellation, error handling,
and eSCL job ownership. CI runs formatting, race-enabled tests, vet, and builds
on Linux and macOS. Bonjour publishing and the native integration tool require macOS.

Optional regressions replay local vendor captures without publishing them:

```sh
make test-captures CAPTURE_DIR=/path/to/private/captures
```

That directory must contain `xerox-overview-scan.pcap` and
`xerox-feeder-2pages.pcap`. These tests are skipped unless
`XEROX_TEST_CAPTURE_DIR` is set. Captures, scans, binaries, logs, and local config
files are ignored by Git and are not needed to build or run the public tests.

For a live check with the bridge running, release any open Image Capture session:

```sh
swift tools/imagecapture-check.swift
swift tools/imagecapture-check.swift --overview
swift tools/imagecapture-check.swift --scan "$PWD/out"
swift tools/imagecapture-check.swift --feeder "$PWD/out" 3
```

The last command expects three loaded sheets and exactly three output files.
If you change the Bonjour name, set `XEROX_SCANNER_NAME` when running the checker.
If Image Capture retains an old “in use” message after a check, Command-click
the scanner to deselect it, then select it again.

## Troubleshooting

```sh
./bin/xerox-airscan serve --verbose
curl http://localhost:8090/eSCL/ScannerCapabilities
curl http://localhost:8090/eSCL/ScannerStatus
dns-sd -B _uscan._tcp local.
```

Allow local network access and incoming connections if macOS prompts. Bonjour
requires multicast between the bridge and client. Reserve a stable printer IP,
or configure a stable hostname. Close other scanner sessions before connecting.
Only one bridge can listen on a given port. `--bonjour=false` disables discovery
for manual HTTP testing. Verbose diagnostics include status replies, never image data.

The service uses unencrypted HTTP without authentication; run it on a trusted
local network. Avoid publishing captures or logs with bug reports without checking
for private information.

A local classic Ethernet/IPv4/TCP pcap can be decoded with:

```sh
./bin/xerox-airscan decode-pcap --capture overview.pcap --output out/overview.png
```

## Project layout

- `cmd/xerox-airscan`: CLI, direct file output, HTTP server lifecycle, and Bonjour.
- `internal/config`: JSON configuration, defaults, and validation.
- `internal/xerox`: device protocol, image assembly, and capture decoding.
- `internal/escl`: capabilities XML, scan settings, status, and HTTP job lifecycle.
- `tools/imagecapture-check.swift`: native Apple driver integration check.
- [PROTOCOL.md](PROTOCOL.md): packet fields and observed page-advance behavior.

The protocol was independently implemented from network captures, with details
cross-checked against [SANE's xerox_mfp backend](https://gitlab.com/sane-project/backends/-/tree/master/backend)
and [AirSane](https://github.com/SimulPiscator/AirSane). Neither is linked or required.

Licensed under the [MIT License](LICENSE).
