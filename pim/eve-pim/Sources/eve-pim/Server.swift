import Darwin
import EvePimCore
import Foundation

struct Options {
    var socketDir = ""
    var peerUser = "_evegw"
    var group = "_evepim"
    var writesEnabled = false
}

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write(Data("eve-pim: \(msg)\n".utf8))
    exit(1)
}

func log(_ msg: String) {
    FileHandle.standardError.write(Data("\(msg)\n".utf8))
}

func parseOptions(_ args: [String]) -> Options {
    var o = Options()
    var it = args.dropFirst().makeIterator()
    while let a = it.next() {
        switch a {
        case "--socket-dir": o.socketDir = it.next() ?? ""
        case "--peer-user": o.peerUser = it.next() ?? ""
        case "--group": o.group = it.next() ?? ""
        case "--writes-enabled": o.writesEnabled = true
        default: fail("unknown argument")
        }
    }
    if o.socketDir.isEmpty || !o.socketDir.hasPrefix("/") { fail("--socket-dir must be an absolute path") }
    return o
}

func lookupUID(_ name: String) -> uid_t? {
    guard let pw = getpwnam(name) else { return nil }
    return pw.pointee.pw_uid
}

func lookupGID(_ name: String) -> gid_t? {
    guard let gr = getgrnam(name) else { return nil }
    return gr.pointee.gr_gid
}

/// serve binds pim.sock and answers one request per connection. The socket
/// is the only listener; the process opens no network connections.
func serve(_ o: Options, dispatcher: Dispatcher) -> Never {
    guard let peerUID = lookupUID(o.peerUser) else { fail("peer user not found") }
    guard let gid = lookupGID(o.group) else { fail("group not found") }
    do {
        try PathGuard.checkDir(o.socketDir, owner: getuid(), group: gid)
    } catch {
        fail("\(error)")
    }
    let path = o.socketDir + "/pim.sock"
    do { try PathGuard.refuseExisting(path) } catch { fail("\(error)") }

    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    if fd < 0 { fail("socket failed") }
    var addr = sockaddr_un()
    addr.sun_family = sa_family_t(AF_UNIX)
    let maxLen = MemoryLayout.size(ofValue: addr.sun_path)
    guard path.utf8.count < maxLen else { fail("socket path too long") }
    withUnsafeMutablePointer(to: &addr.sun_path) { p in
        p.withMemoryRebound(to: CChar.self, capacity: maxLen) { strncpy($0, path, maxLen - 1) }
    }
    let old = umask(0o117) // socket created as 0660
    let rc = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
        }
    }
    umask(old)
    if rc != 0 { fail("bind failed errno=\(errno)") }
    if chmod(path, 0o660) != 0 { fail("chmod failed") }
    // BSD semantics: the socket inherits the directory's group (_evepim).
    do { try PathGuard.checkSocket(path, group: gid) } catch { fail("\(error)") }
    if listen(fd, 8) != 0 { fail("listen failed") }
    log("eve-pim listening writes_enabled=\(o.writesEnabled)")

    signal(SIGPIPE, SIG_IGN)
    while true {
        let c = accept(fd, nil, nil)
        if c < 0 { continue }
        handleConn(c, peerUID: peerUID, dispatcher: dispatcher)
        close(c)
    }
}

func handleConn(_ c: Int32, peerUID: uid_t, dispatcher: Dispatcher) {
    var uid: uid_t = 0, gid: gid_t = 0
    guard getpeereid(c, &uid, &gid) == 0, PeerPolicy.allowed(peerUID: uid, allowlist: [peerUID]) else {
        log("eve-pim request method=- status=peer_denied")
        return
    }
    var tv = timeval(tv_sec: 2, tv_usec: 0)
    setsockopt(c, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
    setsockopt(c, SOL_SOCKET, SO_SNDTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
    var buf = Data()
    var chunk = [UInt8](repeating: 0, count: 4096)
    while buf.count <= Limits.maxRequestBytes {
        let n = read(c, &chunk, chunk.count)
        if n <= 0 { break }
        buf.append(contentsOf: chunk[0..<n])
        if chunk[n - 1] == UInt8(ascii: "\n") { break }
    }
    let (resp, rec) = dispatcher.handle(buf)
    log(rec.line)
    resp.withUnsafeBytes { p in
        var off = 0
        while off < resp.count {
            let w = write(c, p.baseAddress! + off, resp.count - off)
            if w <= 0 { break }
            off += w
        }
    }
}
