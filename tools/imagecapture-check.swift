// Native macOS integration check. Start xerox-airscan serve first.
// swift tools/imagecapture-check.swift [--overview|--scan|--feeder] [output-directory] [expected-pages]
import AppKit
import Foundation
import ImageCaptureCore

final class Check: NSObject, ICDeviceBrowserDelegate, ICScannerDeviceDelegate {
    let browser = ICDeviceBrowser()
    var selected: ICScannerDevice?
    var started = false
    var saved = 0
    var scanRequested = false
    let mode = CommandLine.arguments.dropFirst().first ?? "--discover"
    let scannerName = ProcessInfo.processInfo.environment["XEROX_SCANNER_NAME"] ?? "Xerox WorkCentre 3225 AirScan"

    func start() {
        browser.delegate = self
        browser.browsedDeviceTypeMask = ICDeviceTypeMask(rawValue: ICDeviceTypeMask.scanner.rawValue | ICDeviceLocationTypeMask.bonjour.rawValue | ICDeviceLocationTypeMask.local.rawValue)!
        browser.start()
    }
    func deviceBrowser(_ browser: ICDeviceBrowser, didAdd device: ICDevice, moreComing: Bool) {
        print("Discovered: \(device.name ?? "unnamed")")
        guard selected == nil, let scanner = device as? ICScannerDevice,
              scanner.name == scannerName else { return }
        selected = scanner
        scanner.delegate = self
        scanner.requestOpenSession()
    }
    func deviceBrowser(_ browser: ICDeviceBrowser, didRemove device: ICDevice, moreGoing: Bool) {}
    func didRemove(_ device: ICDevice) { fail("device removed") }
    func device(_ device: ICDevice, didOpenSessionWithError error: Error?) {
        if let error = error { fail("open session: \(error)") }
        print("Opened session through Apple's scanner driver")
    }
    func deviceDidBecomeReady(_ device: ICDevice) {
        guard let scanner = device as? ICScannerDevice, !started else { return }
        started = true
        if mode == "--feeder" {
            guard scanner.availableFunctionalUnitTypes.contains(NSNumber(value: ICScannerFunctionalUnitType.documentFeeder.rawValue)) else { fail("feeder not advertised") }
            DispatchQueue.main.async { scanner.requestSelect(.documentFeeder) }
            return
        }
        begin(scanner)
    }
    func scannerDevice(_ scanner: ICScannerDevice, didSelect functionalUnit: ICScannerFunctionalUnit, error: Error?) {
        if let error = error { fail("select functional unit: \(error)") }
        if mode == "--feeder" && functionalUnit.type == .documentFeeder { begin(scanner) }
    }
    func begin(_ scanner: ICScannerDevice) {
        guard !scanRequested else { return }
        scanRequested = true
        let unit = scanner.selectedFunctionalUnit
        print("Ready: resolutions=\(unit.supportedResolutions), size=\(unit.physicalSize), state=\(unit.state.rawValue), overview=\(unit.canPerformOverviewScan)")
        switch mode {
        case "--overview":
            unit.overviewResolution = 75
            DispatchQueue.main.async { scanner.requestOverviewScan() }
        case "--scan", "--feeder":
            unit.resolution = 300
            unit.measurementUnit = .inches
            unit.scanArea = CGRect(origin: .zero, size: unit.physicalSize)
            scanner.transferMode = .fileBased
            let directory = CommandLine.arguments.count > 2 ? CommandLine.arguments[2] : FileManager.default.currentDirectoryPath + "/out"
            scanner.downloadsDirectory = URL(fileURLWithPath: directory, isDirectory: true)
            scanner.documentName = mode == "--feeder" ? "imagecapture-feeder-300dpi" : "imagecapture-300dpi"
            if let feeder = unit as? ICScannerFunctionalUnitDocumentFeeder {
                feeder.duplexScanningEnabled = false
                print("Feeder loaded: \(feeder.documentLoaded)")
            }
            scanner.documentUTI = "public.png"
            DispatchQueue.main.async { scanner.requestScan() }
        default:
            scanner.requestCloseSession()
        }
    }
    func scannerDevice(_ scanner: ICScannerDevice, didCompleteOverviewScanWithError error: Error?) {
        if let error = error { fail("overview: \(error)") }
        guard let image = scanner.selectedFunctionalUnit.overviewImage else { fail("no overview image") }
        print("Image Capture overview succeeded: \(image.width)x\(image.height)")
        scanner.requestCloseSession()
    }
    func scannerDevice(_ scanner: ICScannerDevice, didScanTo url: URL) {
        saved += 1
        print("Image Capture saved: \(url.path)")
    }
    func scannerDevice(_ scanner: ICScannerDevice, didCompleteScanWithError error: Error?) {
        if let error = error { fail("scan: \(error)") }
        if saved == 0 { fail("scan completed without an output file") }
        if CommandLine.arguments.count > 3, let expected = Int(CommandLine.arguments[3]), saved != expected {
            fail("expected \(expected) pages but Apple's driver delivered \(saved)")
        }
        print("Completed \(saved) page(s)")
        scanner.requestCloseSession()
    }
    func device(_ device: ICDevice, didCloseSessionWithError error: Error?) {
        if let error = error { fail("close session: \(error)") }
        print("PASS")
        exit(0)
    }
    func device(_ device: ICDevice, didEncounterError error: Error?) {
        if let error = error { fail("scanner: \(error)") }
    }
    func fail(_ message: String) -> Never {
        fputs("FAIL: \(message)\n", stderr)
        exit(1)
    }
}

setbuf(stdout, nil)
_ = NSApplication.shared
NSApplication.shared.setActivationPolicy(.prohibited)
let check = Check()
check.start()
RunLoop.main.run(until: Date(timeIntervalSinceNow: 180))
fputs("FAIL: timed out waiting for Image Capture\n", stderr)
exit(1)
