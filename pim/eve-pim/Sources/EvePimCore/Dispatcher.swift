import Foundation

/// One content-free log record: method, item count and error code only.
public struct LogRecord: Equatable, Sendable {
    public var method: String
    public var count: Int?
    public var error: String?

    public var line: String {
        var s = "eve-pim request method=\(method)"
        if let count { s += " count=\(count)" }
        s += " status=" + (error ?? "ok")
        return s
    }
}

public final class Dispatcher {
    let store: PimStore
    let writesEnabled: Bool
    let tz: TimeZone

    public init(store: PimStore, writesEnabled: Bool, timeZone: TimeZone = .current) {
        self.store = store
        self.writesEnabled = writesEnabled
        self.tz = timeZone
    }

    /// handle parses, executes and encodes one request. It never throws.
    public func handle(_ raw: Data) -> (response: Data, log: LogRecord) {
        let rid = requestID(raw)
        let parsed: ParsedRequest
        do {
            parsed = try parseRequest(raw)
        } catch let e as PimError {
            return (encodeError(rid, e.code), LogRecord(method: "-", count: nil, error: e.code.rawValue))
        } catch {
            return (encodeError(rid, .invalidRequest), LogRecord(method: "-", count: nil, error: PimErrorCode.invalidRequest.rawValue))
        }
        let m = parsed.request.method.rawValue
        if parsed.request.method.isWrite && !writesEnabled {
            return (encodeError(parsed.id, .writesDisabled), LogRecord(method: m, count: nil, error: PimErrorCode.writesDisabled.rawValue))
        }
        do {
            let (result, count) = try execute(parsed.request)
            return (encodeOK(parsed.id, result), LogRecord(method: m, count: count, error: nil))
        } catch let e as PimError {
            return (encodeError(parsed.id, e.code), LogRecord(method: m, count: nil, error: e.code.rawValue))
        } catch {
            return (encodeError(parsed.id, .internalError), LogRecord(method: m, count: nil, error: PimErrorCode.internalError.rawValue))
        }
    }

    private func execute(_ req: PimRequest) throws -> ([String: Any], Int) {
        switch req {
        case let .listEvents(from, to):
            var evs = try store.events(from: from, to: to, limit: Limits.maxEvents + 1)
            evs.sort { $0.start < $1.start }
            let truncated = evs.count > Limits.maxEvents
            let out = evs.prefix(Limits.maxEvents).map(eventJSON)
            return (["events": Array(out), "truncated": truncated], out.count)
        case let .listReminders(dueBefore, includeDone):
            var rs = try store.reminders(dueBefore: dueBefore, includeDone: includeDone, limit: Limits.maxReminders + 1)
            if let dueBefore { rs = rs.filter { ($0.due ?? .distantFuture) < dueBefore } }
            if !includeDone { rs = rs.filter { !$0.done } }
            let truncated = rs.count > Limits.maxReminders
            let out = rs.prefix(Limits.maxReminders).map(reminderJSON)
            return (["reminders": Array(out), "truncated": truncated], out.count)
        case let .addReminder(title, due, list):
            return (["reminder": reminderJSON(try store.addReminder(title: title, due: due, list: list))], 1)
        case let .completeReminder(id):
            return (["reminder": reminderJSON(try store.completeReminder(id: id))], 1)
        case .listCalendars:
            let cs = try store.calendars()
            return (["calendars": cs.map { ["id": $0.id, "name": $0.name, "writable": $0.writable] as [String: Any] }], cs.count)
        case let .addEvent(title, start, end, allDay, location, notes, calendarID):
            let ev = try store.addEvent(title: title, start: start, end: end, allDay: allDay,
                                        location: location, notes: notes, calendarID: calendarID)
            return (["event": eventJSON(ev)], 1)
        }
    }

    private func eventJSON(_ e: PimEvent) -> [String: Any] {
        ["id": e.id, "title": e.title, "start": RFC3339.format(e.start, tz: tz),
         "end": RFC3339.format(e.end, tz: tz), "all_day": e.allDay, "calendar": e.calendar, "calendar_id": e.calendarID]
    }

    private func reminderJSON(_ r: PimReminder) -> [String: Any] {
        ["id": r.id, "title": r.title, "due": r.due.map { RFC3339.format($0, tz: tz) } ?? NSNull(),
         "list": r.list, "done": r.done]
    }

    private func encodeOK(_ id: String, _ result: [String: Any]) -> Data {
        encode(["v": 1, "id": id, "ok": true, "result": result])
    }

    private func encodeError(_ id: String, _ code: PimErrorCode) -> Data {
        encode(["v": 1, "id": id, "ok": false, "error": code.rawValue])
    }

    private func encode(_ obj: [String: Any]) -> Data {
        var d = (try? JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys])) ?? Data(#"{"ok":false,"error":"internal","v":1,"id":""}"#.utf8)
        d.append(UInt8(ascii: "\n"))
        return d
    }
}
