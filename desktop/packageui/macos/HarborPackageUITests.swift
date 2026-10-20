import AppKit
import XCTest

final class HarborPackageUITests: XCTestCase {
    private let cli = "/usr/local/bin/harbor"
    private let installedApplication = URL(fileURLWithPath: "/Applications/Harbor.app")
    private let projectName = "Harbor Installed UI"
    private var application: XCUIApplication!
    private var launchCommands: [[String]] = []

    override func setUpWithError() throws {
        continueAfterFailure = false
        executionTimeAllowance = 600
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
        try click("Start project", timeout: 30)
        try awaitProject(identifier, state: "ready")
        try assertHTTPS(slug)
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
        try launchInstalledApplication("second-launch")
        try awaitWindow()
        try waitUntil("single-instance handoff completes") {
            let running = NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor")
            return running.count == 1 && running.first?.processIdentifier == originalProcess
        }
        XCTAssertEqual(try installedProcess(), originalProcess, "second launch must restore the existing window")
        try assertProject(identifier, state: "ready")
        attach("visible-second-launch-original-process")

        try quitInstalledApplication()
        try assertProject(identifier, state: "ready")
        try launchInstalledApplication("relaunch")
        try awaitWindow()
        try assertInstalledBundle()
        XCTAssertNotEqual(try installedProcess(), originalProcess, "relaunch must create a new native UI process")
        try assertProject(identifier, state: "ready")
        try assertHTTPS(slug)
        attach("reopened-running-project")

        if !application.buttons["Stop project"].waitForExistence(timeout: 5) {
            try click("Projects")
            let row = application.links.matching(NSPredicate(format: "label CONTAINS %@", projectName)).firstMatch
            XCTAssertTrue(row.waitForExistence(timeout: 20))
            row.click()
        }
        try click("Stop project", timeout: 30)
        try awaitProject(identifier, state: "stopped")
        try quitInstalledApplication()
        try assertProject(identifier, state: "stopped")
        let evidence: [String: Any] = [
            "application": installedApplication.path,
            "project_directory": projectDirectory.path,
            "project_id": identifier,
            "creation": "production graphical wizard",
            "start": "ready with system-trusted HTTPS 200",
            "window_close": "project remains ready",
            "window_reopen": "ordinary macOS open restores the original process and visible window",
            "second_launch": "forced new instance while visible exits and retains original foreground process",
            "launch_commands": launchCommands,
            "ui_quit": "daemon and project remain ready",
            "ui_reopen": "registration retained and HTTPS 200",
            "final_project_state": "stopped",
        ]
        try JSONSerialization.data(withJSONObject: evidence, options: [.prettyPrinted, .sortedKeys])
            .write(to: URL(fileURLWithPath: "/tmp/harbor-package-ui-evidence.json"), options: .withoutOverwriting)
    }

    private func awaitWindow() throws {
        try waitUntil("installed Harbor window", timeout: 60) { self.application.windows.firstMatch.isHittable }
    }

    // launchInstalledApplication retains native stderr even if startup exits before a window appears.
    private func launchInstalledApplication(_ stage: String, newInstance: Bool = true) throws {
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

    private func assertInstalledBundle() throws {
        let running = try XCTUnwrap(NSRunningApplication.runningApplications(withBundleIdentifier: "com.goforj.harbor").first)
        XCTAssertEqual(running.bundleURL?.resolvingSymlinksInPath(), installedApplication.resolvingSymlinksInPath())
    }

    private func click(_ label: String, timeout: TimeInterval = 20) throws {
        let button = application.buttons[label]
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
        try waitUntil("project \(identifier) reaches \(state)", timeout: 180) {
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
        let state = XCTAttachment(string: "application_state=\(application.state.rawValue)\n" + processes.joined(separator: "\n") + "\nwindows:\n" + application.windows.debugDescription)
        state.name = name + "-native-state"
        state.lifetime = .keepAlways
        add(state)
    }
}
