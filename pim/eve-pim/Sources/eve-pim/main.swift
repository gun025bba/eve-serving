import EvePimCore
import Foundation

// eve-pim: Jarvis M1 EventKit helper. Runs as a LaunchAgent in the owner's
// login session (TCC). See pim/eve-pim/README.md.
let opts = parseOptions(CommandLine.arguments)
let store = EventKitStore()
store.requestAccess()
serve(opts, dispatcher: Dispatcher(store: store, writesEnabled: opts.writesEnabled))
