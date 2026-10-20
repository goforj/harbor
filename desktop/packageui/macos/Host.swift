import AppKit

// Xcode requires a target application for its UI runner; the tests explicitly launch the installed Harbor bundle.
@main
struct PackageUITestHost {
    @MainActor
    static func main() {
        let application = NSApplication.shared
        application.setActivationPolicy(.accessory)
        application.run()
    }
}
