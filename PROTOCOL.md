# Xerox WorkCentre 3225 scan protocol

This document describes the port-9400 protocol implemented by this bridge and
its translation to AirScan/eSCL. It is based on flatbed and two-page feeder
captures, live printer checks, and the current implementation. It is not a vendor
specification. Unidentified fields are marked explicitly; other Xerox models
have not been validated.

All offsets below are **zero-based decimal byte offsets**, unless prefixed with
`0x`. Multibyte numeric fields are big endian unless stated otherwise. Byte
values and command/state codes are hexadecimal; dimensions and counts are decimal.

The scanner endpoint comes from `scanner_address` in the local configuration.
TCP port 9400 is the default. Private addresses, device identifiers, scanned
pages, and reference pcaps are not included in the public repository.

## Transport and framing

Commands and replies share a TCP connection. TCP packet boundaries have no
protocol significance: a reply or JPEG block may span several packets, and
multiple packets may arrive in one read. Read each response to its declared size
before issuing the next command.

A normal four-byte request is:

```text
1b a8 COMMAND 00
```

Requests with payloads normally use `1b a8 COMMAND LENGTH` followed by the payload.
`SET_WINDOW` is an exception: its length byte is `13`, but its complete request
is **25 bytes**, rather than `4 + 0x13` bytes.

Control replies begin:

```text
a8 STATUS LENGTH MESSAGE ...
```

The complete reply size is `LENGTH + 3`. `INQUIRY` returns 70 bytes (`LENGTH=43`);
other implemented control commands return 32 (`LENGTH=1d`). `READ_IMAGE` has no
control reply: it transfers the block previously announced by `READ`.

| Command | Code | Operation |
|---|---|---|
| INQUIRY | `12` | Read identity, geometry, resolutions, and paper status |
| RESERVE | `16` | Acquire the scanner |
| SET_WINDOW | `24` | Configure acquisition |
| OBJECT_POSITION | `31` | Start a flatbed scan or advance/start a feeder page |
| READ | `28` | Poll for and describe the next image strip |
| READ_IMAGE | `29` | Transfer the announced strip block |
| ABORT | `06` | Stop an acquisition/batch |
| RELEASE | `17` | Release the reservation |

Command handling is in [internal/xerox/protocol.go](internal/xerox/protocol.go).

## Identity and capabilities

`INQUIRY` must return header `a8 00 43 10`. The bridge reads these fields:

| Offset | Size | Interpretation |
|---|---|---|
| 4–35 | 32 | Space-padded manufacturer/model text |
| 36–37, 55 | 3 total | Resolution mask: `(reply[55] << 16) \| (reply[36] << 8) \| reply[37]` |
| 38 | 1 | Low two bits participate in paper-loaded detection |
| 40 | 4 | Maximum width, in 1/1200 inch units |
| 50 | 1 | Compression capabilities; mask `40` must be set for JPEG acquisition |
| 53 | 1 | Paper-loaded detection requires this byte to equal `02` |
| 56 | 4 | Maximum feeder height, in 1/1200 inch units |
| 60 | 4 | Maximum flatbed height, in 1/1200 inch units |

The implemented loaded-paper predicate is:

```text
reply[53] == 0x02 && (reply[38] & 0x03) != 0
```

It changed correctly during loaded/empty feeder checks. An empty input tray
alone does not prove that an active batch has no buffered pages; batch completion
is determined by the next `OBJECT_POSITION` response.

The bridge accepts these resolution bits and translates them to window codes:

| DPI | Resolution-mask bit | SET_WINDOW code |
|---|---|---|
| 75 | 0 | `00` |
| 100 | 12 | `0a` |
| 150 | 1 | `02` |
| 200 | 4 | `09` |
| 300 | 5 | `05` |
| 600 | 8 | `07` |

Only resolutions reported by the connected printer are advertised and accepted.

## Acquisition window

The complete `SET_WINDOW` request starts `1b a8 24 13 30`:

| Offset | Size | Interpretation |
|---|---|---|
| 4 | 1 | Parameter message `30` |
| 5 | 4 | Width, in 1/1200 inch units |
| 9 | 4 | Height, in 1/1200 inch units |
| 13, 14 | 1 each | X and Y resolution codes |
| 15, 16 | 1 each | X offset: whole inches, then hundredths of an inch |
| 17, 18 | 1 each | Y offset: whole inches, then hundredths of an inch |
| 19 | 1 | Composition `05`: RGB24 |
| 20 | 1 | Compression `06`: lossy JPEG |
| 21 | 1 | `01` in both vendor captures; precise meaning unconfirmed |
| 22 | 1 | Threshold parameter; bridge retains captured value `02` |
| 23 | 1 | Source `40`: flatbed; `20`: feeder |
| 24 | 1 | `00` in both captures; precise meaning unconfirmed |

The bridge always acquires color JPEG strips, including for grayscale output.
Grayscale conversion happens after JPEG decoding. Other composition/compression
modes are not implemented.

### Coordinates and cropping

The device uses 1/1200 inch units; eSCL regions use 1/300 inch units. A region's
width and height are therefore multiplied by four for the device window.

Hardware offsets have only 1/100 inch precision. The bridge instead leaves all
hardware offsets zero and acquires from the origin to the requested region's
bottom-right corner:

```text
device_width  = 4 * (XOffset + Width)
device_height = 4 * (YOffset + Height)

pixel_left   = floor(XOffset * DPI / 300)
pixel_top    = floor(YOffset * DPI / 300)
pixel_right  = floor((XOffset + Width) * DPI / 300)
pixel_bottom = floor((YOffset + Height) * DPI / 300)
```

Decoded strips are copied into that crop rectangle. The requested region must
fit the selected source, be at least four eSCL units wide and high, and use equal
X/Y resolutions. Settings remain fixed throughout a feeder batch.

Observed geometry:

| Dimension | Device units | eSCL units |
|---|---|---|
| Width | 10208 | 2552 |
| Flatbed height | 14040 | 3510 |
| Feeder maximum height | 16800 | 4200 |

These values come from the printer, not from the size requested in a capture.

## Status and error handling

The control-reply status byte is at offset 1:

| Status | Meaning | Bridge behavior |
|---|---|---|
| `00` | Good | Interpret the reply message |
| `02` | Check scanner state | Decode the state bitmask |
| `04` | Canceled | Return cancellation |
| `08` | Busy | Retry commands using the wait path after 150 ms |

For status `02`, the state bitmask is at offsets **12–13 for READ**, and **4–5
for other commands**.

| State bit | Meaning | Current handling |
|---|---|---|
| `0001` | No error | Continue when this is the sole state bit |
| `0010` | No document | Empty feeder/end of batch |
| `0020` | Document jam | Return feeder-jam error |
| `0040` | Cover open | Return cover-open error |
| `0080` | Warming | Retry as busy |
| `0200` | Invalid region | Return scanner-state error |
| `0400` | Resource busy | Retry as busy |

Jam and cover-open take precedence over no-document when bits are combined.
Unrecognized statuses or states fail explicitly. A no-document error after some
rows but before the final strip is an incomplete-page error, not a successful
end of batch.

Normal acquisition commands use a 20-second I/O deadline. Busy polling is bounded
by the enclosing job/request context. Cancellation closes the TCP connection to
interrupt blocked reads. When closing a reserved, still-live connection, the
bridge sends ABORT for an incomplete acquisition and then RELEASE; a successfully
completed page can be released without ABORT.

## JPEG strips and page completion

A successful `READ` describes one image block:

| Offset | Size | Interpretation |
|---|---|---|
| 3 | 1 | `80`: more strips; `81`: last strip of this page |
| 4 | 4 | Complete block transfer length |
| 8 | 2 | Valid row count in this strip |
| 10 | 2 | Pixel width |

After this reply, issue `1b a8 29 00` and read exactly the announced number of
bytes. Blocks contain a JPEG image plus alignment/transport padding. Consume the
whole block, but treat only decoded JPEG pixels as image data. Do not concatenate
JPEG strips into a single JPEG file.

The bridge decodes each strip separately, appends its valid rows, and discards
JPEG rows beyond the reported count. JPEG decoding stops at EOI; trailing bytes
are not interpreted as another control reply. Up to 512 leading zero bytes are
accepted before SOI as a compatibility allowance. Nonzero leading garbage,
inconsistent dimensions, and missing JPEG headers are rejected.

Implementation guardrails include a maximum 32 MiB block, 7200-pixel strip width,
4096 valid rows per strip, and 10000 strips per page. These are bridge bounds,
not claims about the vendor protocol's theoretical limits.

A flatbed result shorter than the requested crop is an error. A feeder page may
end before its maximum requested height: the paper sensor determines the actual
sheet length. The bridge preserves those received rows, provided the page extends
past the requested crop's top edge and has a valid final strip. Extra rows beyond the
requested crop are discarded.

## Flatbed and feeder command sequences

A successful flatbed scan uses:

```text
INQUIRY → RESERVE → SET_WINDOW(source=40) → OBJECT_POSITION
  repeat READ / READ_IMAGE until message 81
RELEASE
```

The vendor's overview capture additionally attempts OBJECT_POSITION after the
page, receives no-document, and sends ABORT/RELEASE. The bridge does not need that
extra page-advance attempt to finish a flatbed scan.

A feeder batch requires **one connection and reservation for the entire batch**:

```text
INQUIRY → RESERVE → SET_WINDOW(source=20)
  OBJECT_POSITION → READ / READ_IMAGE ... → final strip 81   [page 1]
  OBJECT_POSITION → READ / READ_IMAGE ... → final strip 81   [page 2]
  ...
  OBJECT_POSITION → status 02, state 0010                    [empty]
ABORT → RELEASE → close connection
```

OBJECT_POSITION may first report busy while the feeder finishes moving paper.
Retry until it succeeds or reports a terminal state. Do not repeat INQUIRY,
RESERVE, or SET_WINDOW between pages, and do not close/release the connection at
a page boundary. READ alone after a completed page was observed to return a good
reply with message `00` and no image data; it does not advance the feeder.

## AirScan/eSCL translation

The bridge publishes `_uscan._tcp` with `rs=eSCL`. TXT records describe JPEG/PNG
formats, color/grayscale, platen/ADF availability, simplex operation, and a stable
UUID derived from the configured scanner address. The reported serial field is
also a bridge-generated identifier, not the printer's hardware serial number.

| Endpoint | Purpose |
|---|---|
| `GET /eSCL/ScannerCapabilities` | Model, sources, geometry, modes, formats, resolutions |
| `GET /eSCL/ScannerStatus` | Idle/processing state, feeder state, and job summaries |
| `POST /eSCL/ScanJobs` | Validate settings and create a job; return `201` plus Location |
| `GET /eSCL/ScanJobs/{id}/NextDocument` | Acquire, assemble, encode, and return one page |
| `GET /eSCL/ScanJobs/{id}` | Return that job's status |
| `DELETE /eSCL/ScanJobs/{id}` | Cancel the job |

Acquisition begins on NextDocument. The bridge buffers and encodes a complete
page before returning it as `image/jpeg` or `image/png`. It accepts `RGB24` and
`Grayscale8`, one region, and sources `Platen` or `Feeder`. PDF creation, combining
pages, and other client-side output processing belong to Image Capture.

Only one job owns the scanner at a time. Concurrent job creation or concurrent
page retrieval receives `503` with Retry-After. Invalid settings receive `400`,
unknown jobs `404`, and subsequent requests for finished jobs `410`. Empty-feeder
page retrieval returns `410`; it completes a batch only if at least one page was
already delivered. Other acquisition failures return `502` and abort the job.

Idle feeder status is refreshed with a short unreserved INQUIRY and cached for
two seconds. Status polling does not query the device while a job owns it. A job's
five-minute idle/acquisition timer resets when a page starts and after each feeder
page completes. Request disconnection or DELETE cancels the acquisition context.

The HTTP service is unencrypted and unauthenticated; it is intended for a trusted
local network. See [README.md](README.md) for configuration and operation.

## Evidence and regression coverage

| Check | Observed result |
|---|---|
| Vendor flatbed overview capture | 79 commands; 28 JPEG strips; 638 × 880 received pixels |
| Live 75 dpi full-bed scan | 638 × 877 output pixels after cropping |
| Vendor two-page feeder capture | 471 commands; 218 strips; each page 2480 × 3488 at 300 dpi |
| Live Apple-driver feeder batch | Three 300 dpi images delivered; clean no-document completion and release |

The overview requested 10208 × 16800 device units, exceeding the reported flatbed
height. Its 27 × 32-row strips and final 16-row strip must not be used to infer
bed geometry. Live feeder page heights included both 3488 and 3496 rows; a shorter
page than the selected paper size is not by itself evidence of a failed transfer.

Public tests generate synthetic images and TCP traffic. Optional private-capture
regressions replay the vendor exchanges, including fragmentation and the exact
feeder page-advance/end-of-paper sequence. Run them with:

```sh
make test-captures CAPTURE_DIR=/path/to/private/captures
```

The directory must contain `xerox-overview-scan.pcap` and
`xerox-feeder-2pages.pcap`. See [internal/xerox/protocol_test.go](internal/xerox/protocol_test.go)
and [internal/xerox/fixtures_test.go](internal/xerox/fixtures_test.go).
