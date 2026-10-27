import XCTest
@testable import EvePimCore

final class FakeStore: PimStore {
    var events: [PimEvent] = []
    var reminders: [PimReminder] = []
    var err: PimError?
    var writes = 0

    func events(from: Date, to: Date, limit: Int) throws -> [PimEvent] {
        if let err { throw err }
        return Array(events.prefix(limit))
    }
    func reminders(dueBefore: Date?, includeDone: Bool, limit: Int) throws -> [PimReminder] {
        if let err { throw err }
        return Array(reminders.prefix(limit))
    }
    func addReminder(title: String, due: Date?, list: String?) throws -> PimReminder {
        writes += 1
        return PimReminder(id: "new", title: title, due: due, list: list ?? "", done: false)
    }
    func completeReminder(id: String) throws -> PimReminder {
        writes += 1
        return PimReminder(id: id, title: "t", due: nil, list: "", done: true)
    }
}

let testdata = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    .appendingPathComponent("testdata/protocol")

func json(_ d: Data) -> [String: Any] {
    (try? JSONSerialization.jsonObject(with: d) as? [String: Any]) ?? [:]
}

final class ProtocolTests: XCTestCase {
    struct Case: Decodable { let name: String; let request: String; let error: String? }

    func testCases() throws {
        let cases = try JSONDecoder().decode([Case].self, from: Data(contentsOf: testdata.appendingPathComponent("cases.json")))
        XCTAssertGreaterThan(cases.count, 10)
        for c in cases {
            do {
                _ = try parseRequest(Data(c.request.utf8))
                XCTAssertNil(c.error, c.name)
            } catch let e as PimError {
                XCTAssertEqual(e.code.rawValue, c.error, c.name)
            }
        }
    }

    // Every request line the Go gateway client emits must parse here
    // (the Go side asserts it produces exactly these lines).
    func testGatewayRequestsParse() throws {
        let raw = try String(contentsOf: testdata.appendingPathComponent("gateway_requests.jsonl"), encoding: .utf8)
        let lines = raw.split(separator: "\n")
        XCTAssertFalse(lines.isEmpty)
        for l in lines {
            XCTAssertNoThrow(try parseRequest(Data((l + "\n").utf8)), String(l))
        }
    }

    func testRange31DaysAllowed() throws {
        let r = #"{"v":1,"id":"x","method":"list_events","params":{"from":"2026-10-01T00:00:00Z","to":"2026-11-01T00:00:00Z"}}"#
        XCTAssertNoThrow(try parseRequest(Data(r.utf8)))
    }
}

final class DispatcherTests: XCTestCase {
    let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    func req(_ method: String, _ params: String = "{}") -> Data {
        Data(#"{"v":1,"id":"q","method":"\#(method)","params":\#(params)}"#.utf8)
    }

    func testEventsTruncatedAt50() {
        let s = FakeStore()
        s.events = (0..<60).map { PimEvent(id: "e\($0)", title: "secret \($0)", start: t0 + Double($0), end: t0 + 60, allDay: false, calendar: "c") }
        let d = Dispatcher(store: s, writesEnabled: false, timeZone: TimeZone(identifier: "Asia/Seoul")!)
        let (resp, log) = d.handle(req("list_events", #"{"from":"2026-10-28T00:00:00Z","to":"2026-10-29T00:00:00Z"}"#))
        let r = json(resp)["result"] as! [String: Any]
        XCTAssertEqual((r["events"] as! [Any]).count, 50)
        XCTAssertEqual(r["truncated"] as? Bool, true)
        XCTAssertEqual(log.count, 50)
        XCTAssertFalse(log.line.contains("secret"))
        let ev = (r["events"] as! [[String: Any]])[0]
        XCTAssertEqual(Set(ev.keys), ["id", "title", "start", "end", "all_day", "calendar"])
    }

    func testRemindersTruncatedAt100AndFiltered() {
        let s = FakeStore()
        s.reminders = (0..<120).map { PimReminder(id: "r\($0)", title: "t", due: t0, list: "l", done: $0 % 2 == 1) }
        let d = Dispatcher(store: s, writesEnabled: false)
        let (resp, _) = d.handle(req("list_reminders", #"{"include_done":true}"#))
        let r = json(resp)["result"] as! [String: Any]
        XCTAssertEqual((r["reminders"] as! [Any]).count, 100)
        XCTAssertEqual(r["truncated"] as? Bool, true)
        let (resp2, _) = d.handle(req("list_reminders"))
        let r2 = json(resp2)["result"] as! [String: Any]
        XCTAssertEqual((r2["reminders"] as! [Any]).count, 51) // 101 fetched, done filtered out
        XCTAssertEqual(r2["truncated"] as? Bool, false)
        let rem = (r2["reminders"] as! [[String: Any]])[0]
        XCTAssertEqual(Set(rem.keys), ["id", "title", "due", "list", "done"])
    }

    func testWritesDisabledByFlag() {
        let s = FakeStore()
        let d = Dispatcher(store: s, writesEnabled: false)
        let (resp, log) = d.handle(req("add_reminder", #"{"title":"buy milk"}"#))
        XCTAssertEqual(json(resp)["error"] as? String, "writes_disabled")
        XCTAssertEqual(s.writes, 0)
        XCTAssertFalse(log.line.contains("milk"))
        let (resp2, _) = d.handle(req("complete_reminder", #"{"id":"x"}"#))
        XCTAssertEqual(json(resp2)["error"] as? String, "writes_disabled")
        XCTAssertEqual(s.writes, 0)
        let on = Dispatcher(store: s, writesEnabled: true)
        let (resp3, _) = on.handle(req("add_reminder", #"{"title":"buy milk"}"#))
        XCTAssertEqual(json(resp3)["ok"] as? Bool, true)
        XCTAssertEqual(s.writes, 1)
    }

    func testStoreErrorsAreCodesOnly() {
        let s = FakeStore()
        s.err = PimError(.permissionDenied)
        let d = Dispatcher(store: s, writesEnabled: false)
        let (resp, log) = d.handle(req("list_reminders"))
        XCTAssertEqual(json(resp)["error"] as? String, "permission_denied")
        XCTAssertEqual(log.line, "eve-pim request method=list_reminders status=permission_denied")
    }

    func testBadRequestEchoesOnlyID() {
        let d = Dispatcher(store: FakeStore(), writesEnabled: false)
        let (resp, log) = d.handle(Data(#"{"v":1,"id":"zz","method":"list_reminders","params":{"query":"private text"}}"#.utf8))
        let o = json(resp)
        XCTAssertEqual(o["error"] as? String, "invalid_request")
        XCTAssertEqual(o["id"] as? String, "zz")
        XCTAssertFalse(String(decoding: resp, as: UTF8.self).contains("private"))
        XCTAssertEqual(log.line, "eve-pim request method=- status=invalid_request")
    }
}

final class GuardTests: XCTestCase {
    var dir: String!

    override func setUp() {
        dir = NSTemporaryDirectory() + "evepim-\(UUID().uuidString)"
        mkdir(dir, 0o750)
        chmod(dir, 0o750)
    }

    override func tearDown() { try? FileManager.default.removeItem(atPath: dir) }

    func testRefuseExistingFileAndSymlink() throws {
        let p = dir + "/pim.sock"
        XCTAssertNoThrow(try PathGuard.refuseExisting(p))
        FileManager.default.createFile(atPath: p, contents: Data())
        XCTAssertThrowsError(try PathGuard.refuseExisting(p)) { XCTAssertEqual($0 as? GuardError, .socketPathExists) }
        XCTAssertTrue(FileManager.default.fileExists(atPath: p), "must not delete")
        let l = dir + "/link.sock"
        symlink("/nonexistent-target", l) // dangling symlink still refused (lstat)
        XCTAssertThrowsError(try PathGuard.refuseExisting(l))
    }

    func testCheckDir() throws {
        XCTAssertNoThrow(try PathGuard.checkDir(dir, owner: getuid(), group: getgid_dir()))
        chmod(dir, 0o770)
        XCTAssertThrowsError(try PathGuard.checkDir(dir, owner: getuid(), group: getgid_dir())) {
            XCTAssertEqual($0 as? GuardError, .dirMode(0o770))
        }
        chmod(dir, 0o750)
        XCTAssertThrowsError(try PathGuard.checkDir(dir, owner: getuid() &+ 1, group: getgid_dir()))
        XCTAssertThrowsError(try PathGuard.checkDir(dir, owner: getuid(), group: getgid_dir() &+ 1))
        let l = dir + "-link"
        symlink(dir, l)
        defer { unlink(l) }
        XCTAssertThrowsError(try PathGuard.checkDir(l, owner: getuid(), group: getgid_dir())) {
            XCTAssertEqual($0 as? GuardError, .dirNotDirectory)
        }
    }

    func getgid_dir() -> gid_t {
        var st = stat()
        lstat(dir, &st)
        return st.st_gid
    }

    func testPeerPolicyExact() {
        XCTAssertTrue(PeerPolicy.allowed(peerUID: 260, allowlist: [260]))
        XCTAssertFalse(PeerPolicy.allowed(peerUID: 0, allowlist: [260]))
        XCTAssertFalse(PeerPolicy.allowed(peerUID: 501, allowlist: [260]))
        XCTAssertFalse(PeerPolicy.allowed(peerUID: 260, allowlist: []))
    }
}
