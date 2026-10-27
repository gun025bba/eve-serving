import Foundation

// Minimal data returned to the gateway (design §1): no notes, attendees,
// location or URL.

public struct PimEvent: Equatable, Sendable {
    public var id: String
    public var title: String
    public var start: Date
    public var end: Date
    public var allDay: Bool
    public var calendar: String
    public init(id: String, title: String, start: Date, end: Date, allDay: Bool, calendar: String) {
        self.id = id; self.title = title; self.start = start; self.end = end; self.allDay = allDay; self.calendar = calendar
    }
}

public struct PimReminder: Equatable, Sendable {
    public var id: String
    public var title: String
    public var due: Date?
    public var list: String
    public var done: Bool
    public init(id: String, title: String, due: Date?, list: String, done: Bool) {
        self.id = id; self.title = title; self.due = due; self.list = list; self.done = done
    }
}

/// PimStore is the EventKit boundary. Implementations throw PimError
/// (permission_denied, not_found, internal) and nothing else.
public protocol PimStore {
    /// Events overlapping [from, to), sorted by start. May return more than
    /// limit; the dispatcher truncates.
    func events(from: Date, to: Date, limit: Int) throws -> [PimEvent]
    func reminders(dueBefore: Date?, includeDone: Bool, limit: Int) throws -> [PimReminder]
    func addReminder(title: String, due: Date?, list: String?) throws -> PimReminder
    func completeReminder(id: String) throws -> PimReminder
}
