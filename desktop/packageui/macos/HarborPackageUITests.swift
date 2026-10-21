import AppKit
import Darwin
import XCTest

final class HarborPackageUITests: XCTestCase {
    private let cli = "/usr/local/bin/harbor"
    private let installedApplication = URL(fileURLWithPath: "/Applications/Harbor.app")
    private let projectName = "Harbor Installed UI"
    private var application: XCUIApplication!
    private var launchCommands: [[String]] = []

    override func setUpWithError() throws {
        continueAfterFailure = false
        executionTimeAllowance = 1200
        XCTAssertTrue(FileManager.default.isExecutableFile(atPath: cli))
        XCTAssertTrue(FileManager.default.fileExists(atPath: installedApplication.path))
        application = XCUIApplication(bundleIdentifier: "com.goforj.harbor")
    }

    override func tearDownWithError() throws {
        if let application, application.state != .notRunning {
            try assertInstalledBundle()
            application.activate()
            application.typeKey("q", modifierFlags: .command)
            XCTAssertTrue(application.wait(for: .notRunning, timeout: 20), "failure cleanup requires the installed UI to quit")
        }
    }

    func testInstalledCreationStartAndReopen() throws {
        let root = URL(fileURLWithPath: "/tmp/harbor-package-ui-" + UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: false)
        let projectDirectory = root.appendingPathComponent("project", isDirectory: true)
        try launchInstalledApplication("first-launch")
        try awaitWindow()
        try assertInstalledBundle()
        XCTAssertTrue(application.buttons["Dismiss getting started guide"].waitForExistence(timeout: 30))
        attach("installed-first-launch")

        let setup = application.buttons["Set up secure networking"]
        if setup.waitForExistence(timeout: 15) {
            setup.click()
        }
        try waitUntil("secure networking", timeout: 90) {
            (try? self.snapshot()["network_setup"] as? [String: Any])?["configured"] as? Bool == true
        }

        try click("New project")
        try fill("Project name", with: projectName)
        try fill("Go module", with: "example.com/harbor-installed-ui")
        try fill("Project directory", with: projectDirectory.path)
        try click("Continue")
        let minimalComponents: Set<String> = ["CLI", "Web API", "Web UI"]
        for label in ["CLI", "Docker", "Mail", "Auth", "OAuth", "Web API", "Web UI", "Metrics", "Observability", "Grafana", "Database (MySQL)", "Database (Postgres)", "Database (SQLite)", "Scheduler", "Cache", "Events", "File Storage", "Background Jobs"] {
            try setCheckbox(label, selected: minimalComponents.contains(label))
        }
        try select("Starter kit", option: "Vue")
        try click("Continue")
        try select("Atlas agent support", option: "Skip")
        try click("Continue")
        attach("creation-review")
        try click("Create project")
        try waitUntil("created project registration", timeout: 240) {
            if self.application.buttons["Review choices"].exists || self.application.buttons["Retry registration"].exists {
                throw NSError(domain: "HarborPackageUI.Creation", code: 1,
                              userInfo: [NSLocalizedDescriptionKey: self.application.debugDescription])
            }
            return (try? self.project(at: projectDirectory.path)) != nil
        }
        let registration = try XCTUnwrap(try project(at: projectDirectory.path))
        let identifier = try XCTUnwrap(registration["id"] as? String)
        let slug = try XCTUnwrap(registration["slug"] as? String)
        XCTAssertEqual(registration["state"] as? String, "stopped")
        XCTAssertTrue(application.buttons["Dismiss getting started guide"].waitForExistence(timeout: 30), "guided creation returns to Overview")
        try click("Start project", timeout: 30)
        try awaitProject(identifier, state: "ready")
        try assertHTTPS(slug)
        try click("Recheck")
        try waitUntil("native prerequisites are ready") {
            self.hasText("Required tools are ready")
        }
        try click("Open app", timeout: 30)
        application.activate()
        try waitUntil("guide App opening completed") {
            self.hasText("4 of 4 steps complete")
        }
        attach("guide-complete")
        try click("Finish setup")
        XCTAssertFalse(application.buttons["Dismiss getting started guide"].exists)
        attach("project-ready")

        let originalProcess = try installedProcess()
        let close = application.windows.firstMatch.buttons["_XCUI:CloseWindow"]
        XCTAssertTrue(close.exists)
        close.click()
        try waitUntil("window hides without stopping the project") {
            !self.application.windows.firstMatch.isHittable
        }
        try assertProject(identifier, state: "ready")
        try launchInstalledApplication("window-reopen", newInstance: false)
        try awaitWindow()
        XCTAssertEqual(try installedProcess(), originalProcess, "ordinary macOS reopen must restore the existing window")
        try assertProject(identifier, state: "ready")
        attach("window-reopened-original-process")
        try launchInstalledApplication("second-launch", nativePresentation: true)
        try waitUntil("single-instance handoff completes") {
            let running = NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor")
            return running.count == 1 && running.first?.processIdentifier == originalProcess
        }
        XCTAssertEqual(try installedProcess(), originalProcess, "second launch must restore the existing window")
        try assertProject(identifier, state: "ready")
        attach("visible-second-launch-original-process")

        try quitNativeInstalledApplication(originalProcess)
        try assertProject(identifier, state: "ready")
        try launchInstalledApplication("relaunch")
        try awaitWindow()
        try assertInstalledBundle()
        XCTAssertNotEqual(try installedProcess(), originalProcess, "relaunch must create a new native UI process")
        try assertProject(identifier, state: "ready")
        try assertHTTPS(slug)
        if !application.buttons["Stop project"].waitForExistence(timeout: 5) {
            try click("Projects")
            let row = application.links.matching(NSPredicate(format: "title CONTAINS %@", projectName)).firstMatch
            XCTAssertTrue(row.waitForExistence(timeout: 20))
            row.click()
        }
        try waitUntil("reopened project Stop control", timeout: 30) { self.application.buttons["Stop project"].exists }
        attach("reopened-running-project")
        try exerciseCrashRecovery(identifier, projectDirectory: projectDirectory)
        try refreshThroughCommandMenu()
        try awaitProject(identifier, state: "ready")
        try waitUntil("live project Stop control after daemon recovery", timeout: 30) {
            self.application.buttons["Stop project"].exists && self.application.buttons["Stop project"].isEnabled
        }
        try assertHTTPS(slug)
        attach("daemon-crash-recovered-live-ui")
        try click("Stop project", timeout: 30)
        try awaitProject(identifier, state: "stopped")
        attach("project-stopped-registration-retained")
        try quitInstalledApplication()
        try assertProject(identifier, state: "stopped")
        let evidence: [String: Any] = [
            "application": installedApplication.path,
            "project_directory": projectDirectory.path,
            "project_id": identifier,
            "creation": "production graphical wizard",
            "onboarding": "installed first-project guide, native tool recheck, and published App opening completed",
            "start": "ready with system-trusted HTTPS 200",
            "window_close": "project remains ready",
            "window_reopen": "ordinary macOS open restores the original process and visible window",
            "second_launch": "forced new instance while visible retains original active process and native on-screen window",
            "launch_commands": launchCommands,
            "ui_quit": "normal native Quit AppleEvent; daemon and project remain ready",
            "ui_reopen": "registration retained and HTTPS 200",
            "final_project_state": "stopped",
            "final_ui_quit": "Command-Q and native process absence",
            "daemon_recovery": "two installed-daemon crashes, healthy peer, interrupted startup settlement and retry",
        ]
        try JSONSerialization.data(withJSONObject: evidence, options: [.prettyPrinted, .sortedKeys])
            .write(to: URL(fileURLWithPath: "/tmp/harbor-package-ui-evidence.json"), options: .withoutOverwriting)
    }

    private func awaitWindow() throws {
        try waitUntil("installed Harbor window", timeout: 60) { self.application.windows.firstMatch.isHittable }
    }

    // canonicalRecoveryProjectDirectory uses kernel path spelling because Foundation preserves the /tmp alias.
    private func canonicalRecoveryProjectDirectory(_ directory: URL, registeredPath: String) throws -> String {
        guard let resolved = realpath(directory.path, nil) else {
            throw NSError(domain: NSPOSIXErrorDomain, code: Int(errno),
                          userInfo: [NSLocalizedDescriptionKey: "cannot resolve the existing graphical fixture"])
        }
        defer { free(resolved) }
        let canonical = String(cString: resolved)
        guard canonical == registeredPath else {
            throw NSError(domain: "HarborPackageUI.Recovery", code: 1,
                          userInfo: [NSLocalizedDescriptionKey: "canonical graphical fixture differs from its registered path"])
        }
        return canonical
    }

    // exerciseCrashRecovery uses the installed daemon while XCTest retains the original native UI process.
    private func exerciseCrashRecovery(_ identifier: String, projectDirectory: URL) throws {
        let environment = ProcessInfo.processInfo.environment
        let tool = try XCTUnwrap(environment["HARBOR_PACKAGE_UI_CRASH_RECOVERY_TOOL"])
        XCTAssertTrue(tool.hasPrefix("/"), "recovery tool must be an absolute workflow-selected executable")
        XCTAssertTrue(FileManager.default.isExecutableFile(atPath: tool))
        let registration = try XCTUnwrap(try project(at: projectDirectory.path))
        XCTAssertEqual(registration["id"] as? String, identifier)
        let registeredPath = try XCTUnwrap(registration["path"] as? String)
        let canonical = try canonicalRecoveryProjectDirectory(projectDirectory, registeredPath: registeredPath)
        let evidence = "/tmp/harbor-package-ui-crash-recovery.json"
        _ = try command(tool, ["--project-id", identifier, "--project-directory", canonical,
                               "--evidence", evidence])
        let data = try Data(contentsOf: URL(fileURLWithPath: evidence))
        let receipt = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(receipt["complete"] as? Bool, true)
        let attachment = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
        attachment.name = "installed-daemon-crash-recovery"
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    // launchInstalledApplication retains native stderr even if startup exits before a window appears.
    private func launchInstalledApplication(_ stage: String, newInstance: Bool = true, nativePresentation: Bool = false) throws {
        let evidence = URL(fileURLWithPath: "/tmp/harbor-package-ui-native-logs", isDirectory: true)
        try FileManager.default.createDirectory(at: evidence, withIntermediateDirectories: true)
        let arguments = (newInstance ? ["-n"] : []) + ["--stdout", evidence.appendingPathComponent(stage + "-stdout.log").path,
                                                       "--stderr", evidence.appendingPathComponent(stage + "-stderr.log").path,
                                                       installedApplication.path]
        launchCommands.append(["/usr/bin/open"] + arguments)
        _ = try command("/usr/bin/open", arguments)
        try waitUntil("single installed application process", timeout: 20) {
            NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor").count == 1
        }
        try assertInstalledBundle()
        let process = try installedProcess()
        let running = try XCTUnwrap(NSRunningApplication(processIdentifier: process))
        XCTAssertTrue(running.activate(options: []), "activate only the admitted installed application")
        if nativePresentation {
            try waitUntil(stage + " native foreground window", timeout: 20) {
                running.isActive && !running.isHidden && !self.nativeWindows(for: process).isEmpty
            }
            XCTAssertEqual(try installedProcess(), process, "native activation must preserve the installed application process")
            return
        }
        // A forced duplicate can leave XCTest associated with the exited process for this bundle identifier.
        application = XCUIApplication(bundleIdentifier: "com.goforj.harbor")
        application.activate()
        XCTAssertEqual(try installedProcess(), process, "activation must not replace an application that exited")
        try waitUntil(stage + " foreground activation", timeout: 20) { self.application.state == .runningForeground }
    }

    private func installedProcess() throws -> pid_t {
        let running = NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor")
        XCTAssertEqual(running.count, 1)
        return try XCTUnwrap(running.first).processIdentifier
    }

    private func quitInstalledApplication() throws {
        application.typeKey("q", modifierFlags: .command)
        XCTAssertTrue(application.wait(for: .notRunning, timeout: 20))
        try waitUntil("native installed application exit", timeout: 20) {
            NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor").isEmpty
        }
    }

    private func quitNativeInstalledApplication(_ process: pid_t) throws {
        try assertInstalledBundle()
        let running = try XCTUnwrap(NSRunningApplication(processIdentifier: process))
        XCTAssertEqual(try installedProcess(), process)
        XCTAssertTrue(running.terminate(), "normal native Quit AppleEvent must be accepted")
        try waitUntil("native Quit AppleEvent process exit", timeout: 20) {
            NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor").isEmpty
        }
    }

    private func nativeWindows(for process: pid_t) -> [[String: Any]] {
        let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
        return windows.compactMap { window in
            guard (window[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value == process,
                  (window[kCGWindowLayer as String] as? NSNumber)?.intValue == 0,
                  let bounds = window[kCGWindowBounds as String] as? [String: Any],
                  let rectangle = CGRect(dictionaryRepresentation: bounds as CFDictionary),
                  rectangle.width >= 420, rectangle.height >= 520 else { return nil }
            return ["pid": process, "layer": 0, "bounds": bounds]
        }
    }

    private func assertInstalledBundle() throws {
        let running = try XCTUnwrap(NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor").first)
        XCTAssertEqual(running.bundleURL?.resolvingSymlinksInPath(), installedApplication.resolvingSymlinksInPath())
    }

    // refreshThroughCommandMenu reaches the visible refresh action even when the narrow layout hides the context pane.
    private func refreshThroughCommandMenu() throws {
        try click("Open command menu")
        let refresh = application.staticTexts["Refresh Harbor state"]
        XCTAssertTrue(refresh.waitForExistence(timeout: 20), "missing command menu refresh action\n\(application.debugDescription)")
        try waitUntil("visible enabled refresh command") { refresh.isEnabled && refresh.isHittable }
        refresh.click()
        try waitUntil("refresh command closes the menu") { !refresh.exists }
    }

    // hasText supports WebKit text carried by AXValue as well as AXLabel.
    private func hasText(_ text: String) -> Bool {
        application.descendants(matching: .any)
            .matching(NSPredicate(format: "label == %@ OR value == %@", text, text)).firstMatch.exists
    }

    private func click(_ label: String, timeout: TimeInterval = 20) throws {
        let button = application.buttons[label].firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: timeout), "missing button: \(label)\n\(application.debugDescription)")
        try waitUntil("enabled button \(label)") { button.isEnabled }
        button.click()
    }

    private func fill(_ label: String, with value: String) throws {
        let field = application.textFields[label]
        XCTAssertTrue(field.waitForExistence(timeout: 20), "missing field: \(label)\n\(application.debugDescription)")
        try focus(field)
        field.typeKey("a", modifierFlags: .command)
        field.typeText(value)
    }

    private func select(_ label: String, option: String) throws {
        let field = application.popUpButtons[label]
        XCTAssertTrue(field.waitForExistence(timeout: 20), "missing select: \(label)\n\(application.debugDescription)")
        try focus(field)
        field.click()
        for character in option.lowercased() {
            application.typeKey(String(character), modifierFlags: [])
        }
        application.typeKey("\r", modifierFlags: [])
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        try waitUntil("select \(option) in \(label)") { field.value as? String == option }
    }

    private func setCheckbox(_ label: String, selected: Bool) throws {
        let checkbox = application.checkBoxes[label]
        XCTAssertTrue(checkbox.waitForExistence(timeout: 20), "missing checkbox: \(label)\n\(application.debugDescription)")
        if try checkboxIsSelected(checkbox) != selected {
            try focus(checkbox)
            checkbox.typeKey(" ", modifierFlags: [])
        }
        try waitUntil("checkbox \(label) reflects its selection", timeout: 10) {
            try self.checkboxIsSelected(checkbox) == selected
        }
    }

    private func checkboxIsSelected(_ checkbox: XCUIElement) throws -> Bool {
        if let value = checkbox.value as? NSNumber { return value.boolValue }
        let value = try XCTUnwrap(checkbox.value as? String)
        XCTAssertTrue(value == "0" || value == "1", "unexpected checkbox value: \(value)")
        return value == "1"
    }

    // focus uses the real keyboard path so WebKit scrolls fields above the fixed dialog footer.
    private func focus(_ element: XCUIElement) throws {
        for _ in 0..<60 {
            if element.value(forKey: "hasKeyboardFocus") as? Bool == true { return }
            application.typeKey("\t", modifierFlags: [])
        }
        XCTFail("could not focus \(element.label)\n\(application.debugDescription)")
        throw NSError(domain: "HarborPackageUI.Focus", code: 1)
    }

    private func snapshot() throws -> [String: Any] {
        let output = try command(cli, ["daemon", "snapshot"])
        return try XCTUnwrap(JSONSerialization.jsonObject(with: Data(output.utf8)) as? [String: Any])
    }

    private func project(at path: String) throws -> [String: Any]? {
        let projects = try snapshot()["projects"] as? [[String: Any]] ?? []
        return projects.first { row in
            guard let directory = row["path"] as? String else { return false }
            return URL(fileURLWithPath: directory).resolvingSymlinksInPath() == URL(fileURLWithPath: path).resolvingSymlinksInPath()
        }
    }

    private func assertProject(_ identifier: String, state: String) throws {
        let projects = try snapshot()["projects"] as? [[String: Any]] ?? []
        XCTAssertEqual(projects.first { $0["id"] as? String == identifier }?["state"] as? String, state)
    }

    private func awaitProject(_ identifier: String, state: String) throws {
        try waitUntil("project \(identifier) reaches \(state)", timeout: state == "ready" ? 360 : 180) {
            guard let projects = try? self.snapshot()["projects"] as? [[String: Any]] else { return false }
            guard let project = projects.first(where: { $0["id"] as? String == identifier }) else { return false }
            if let observed = project["state"] as? String, ["failed", "quarantined", "unavailable"].contains(observed) {
                let attachment = XCTAttachment(data: try JSONSerialization.data(withJSONObject: project, options: [.prettyPrinted, .sortedKeys]), uniformTypeIdentifier: "public.json")
                attachment.name = "failed-project-state"
                attachment.lifetime = .keepAlways
                self.add(attachment)
                self.attach("failed-project-window")
                throw NSError(domain: "HarborPackageUI.Project", code: 1,
                              userInfo: [NSLocalizedDescriptionKey: "project entered \(observed) while waiting for \(state)"])
            }
            return project["state"] as? String == state
        }
    }

    private func assertHTTPS(_ slug: String) throws {
        let code = try command("/usr/bin/curl", ["--silent", "--show-error", "--connect-timeout", "5", "--max-time", "15", "--output", "/dev/null", "--write-out", "%{http_code}", "https://" + slug + ".test"])
        XCTAssertEqual(code, "200")
    }

    private func waitUntil(_ description: String, timeout: TimeInterval = 30, condition: () throws -> Bool) throws {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if try condition() { return }
            Thread.sleep(forTimeInterval: 0.2)
        }
        attach("failure-" + description)
        XCTFail("timed out: \(description)\n\(application.debugDescription)")
        throw NSError(domain: "HarborPackageUI", code: 1, userInfo: [NSLocalizedDescriptionKey: description])
    }

    private func command(_ executable: String, _ arguments: [String]) throws -> String {
        let process = Process()
        let output = Pipe()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        process.standardOutput = output
        process.standardError = output
        try process.run()
        let data = output.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        let text = String(decoding: data, as: UTF8.self)
        XCTAssertEqual(process.terminationStatus, 0, "\(executable) failed: \(text)")
        if process.terminationStatus != 0 {
            throw NSError(domain: "HarborPackageUI.Command", code: Int(process.terminationStatus), userInfo: [NSLocalizedDescriptionKey: text])
        }
        return text
    }

    private func attach(_ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        let running = NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor")
        let processes = running.map { "pid=\($0.processIdentifier) active=\($0.isActive) hidden=\($0.isHidden) terminated=\($0.isTerminated) bundle=\($0.bundleURL?.path ?? "unknown")" }
        let native = running.flatMap { nativeWindows(for: $0.processIdentifier) }
        let nativeDescription = String(data: (try? JSONSerialization.data(withJSONObject: native, options: [.prettyPrinted, .sortedKeys])) ?? Data(), encoding: .utf8) ?? "unavailable"
        let state = XCTAttachment(string: "application_state=\(application.state.rawValue)\n" + processes.joined(separator: "\n") + "\nnative_on_screen_windows:\n" + nativeDescription + "\nXCTest windows:\n" + application.windows.debugDescription)
        state.name = name + "-native-state"
        state.lifetime = .keepAlways
        add(state)
    }
}
