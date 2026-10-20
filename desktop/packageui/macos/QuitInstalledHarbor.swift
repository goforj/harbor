import AppKit
import Foundation

let installedBundle = URL(fileURLWithPath: "/Applications/Harbor.app").resolvingSymlinksInPath()
let applications = NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor")
guard applications.count <= 1 else {
    fatalError("failure recovery cannot admit multiple Harbor instances")
}
for application in applications {
    guard application.bundleURL?.resolvingSymlinksInPath() == installedBundle else {
        fatalError("failure recovery refuses an application outside the verified installed bundle")
    }
    // The normal quit request stops UI reconnection before daemon and package cleanup begin.
    guard application.terminate() else {
        fatalError("the installed application refused its native quit request")
    }
    let deadline = Date().addingTimeInterval(20)
    while !application.isTerminated && Date() < deadline {
        RunLoop.current.run(until: Date().addingTimeInterval(0.1))
    }
    guard application.isTerminated else {
        fatalError("the installed application did not exit after its native quit request")
    }
}
print("Installed Harbor UI absent before recovery: \(applications.count) native quit requests")
