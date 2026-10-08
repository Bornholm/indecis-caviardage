// An in-memory file system with the Node callback API that Go's
// syscall/fs_js.go calls: the Go program reads the model and reads and
// writes documents in it, as on disk. wasm_exec.js keeps it since
// globalThis.fs is set first. Standard output goes to the console.
(() => {
  const S_IFDIR = 0o040000, S_IFREG = 0o100000;
  const C = { O_WRONLY: 1, O_RDWR: 2, O_CREAT: 64, O_EXCL: 128, O_TRUNC: 512, O_APPEND: 1024, O_DIRECTORY: 65536 };
  const nodes = new Map(); // absolute path -> {dir: true} or {data, size}
  const fds = new Map();
  let nextFd = 10;
  for (const d of ["/", "/tmp", "/model"]) nodes.set(d, { dir: true });

  const fail = (code) => Object.assign(new Error(code), { code });
  const norm = (p) => {
    const out = [];
    for (const s of p.split("/")) {
      if (!s || s === ".") continue;
      if (s === "..") out.pop(); else out.push(s);
    }
    return "/" + out.join("/");
  };
  const parent = (p) => p.slice(0, p.lastIndexOf("/")) || "/";
  const stat = (n) => {
    const now = Date.now();
    return {
      dev: 0, ino: 0, mode: n.dir ? S_IFDIR | 0o755 : S_IFREG | 0o644, nlink: 1, uid: 0, gid: 0, rdev: 0,
      size: n.dir ? 0 : n.size, blksize: 4096, blocks: 0, atimeMs: now, mtimeMs: now, ctimeMs: now,
      isDirectory: () => !!n.dir,
    };
  };
  const grow = (n, size) => {
    if (size > n.data.length) {
      const b = new Uint8Array(Math.max(size, n.data.length * 2, 4096));
      b.set(n.data.subarray(0, n.size));
      n.data = b;
    }
  };
  const decoder = new TextDecoder();
  let line = "";

  globalThis.fs = {
    constants: C,
    // addFile puts a file in the file system without copying it.
    addFile(path, bytes) { nodes.set(norm(path), { data: bytes, size: bytes.length }); },

    writeSync(fd, buf) {
      if (fd !== 1 && fd !== 2) throw fail("EBADF");
      line += decoder.decode(buf);
      let i;
      while ((i = line.indexOf("\n")) >= 0) {
        console.log(line.slice(0, i));
        line = line.slice(i + 1);
      }
      return buf.length;
    },
    write(fd, buf, offset, length, position, cb) {
      if (fd === 1 || fd === 2) return cb(null, this.writeSync(fd, buf.subarray(offset, offset + length)));
      const f = fds.get(fd);
      if (!f) return cb(fail("EBADF"));
      const n = f.node, at = position ?? (f.append ? n.size : f.pos);
      grow(n, at + length);
      n.data.set(buf.subarray(offset, offset + length), at);
      n.size = Math.max(n.size, at + length);
      if (position == null) f.pos = at + length;
      cb(null, length);
    },
    read(fd, buf, offset, length, position, cb) {
      const f = fds.get(fd);
      if (!f) return cb(fail("EBADF"));
      const n = f.node, at = position ?? f.pos, k = Math.max(0, Math.min(length, n.size - at));
      buf.set(n.data.subarray(at, at + k), offset);
      if (position == null) f.pos = at + k;
      cb(null, k);
    },
    open(path, flags, mode, cb) {
      path = norm(path);
      let n = nodes.get(path);
      if (n && flags & C.O_CREAT && flags & C.O_EXCL) return cb(fail("EEXIST"));
      if (!n) {
        if (!(flags & C.O_CREAT) || !nodes.get(parent(path))?.dir) return cb(fail("ENOENT"));
        n = { data: new Uint8Array(0), size: 0 };
        nodes.set(path, n);
      }
      if (flags & C.O_TRUNC && !n.dir) n.size = 0;
      const fd = nextFd++;
      fds.set(fd, { node: n, pos: 0, append: !!(flags & C.O_APPEND) });
      cb(null, fd);
    },
    close(fd, cb) { fds.delete(fd); cb(null); },
    fstat(fd, cb) { const f = fds.get(fd); f ? cb(null, stat(f.node)) : cb(fail("EBADF")); },
    stat(path, cb) { const n = nodes.get(norm(path)); n ? cb(null, stat(n)) : cb(fail("ENOENT")); },
    lstat(path, cb) { this.stat(path, cb); },
    mkdir(path, perm, cb) {
      path = norm(path);
      if (nodes.has(path)) return cb(fail("EEXIST"));
      if (!nodes.get(parent(path))?.dir) return cb(fail("ENOENT"));
      nodes.set(path, { dir: true });
      cb(null);
    },
    readdir(path, cb) {
      path = norm(path);
      if (!nodes.get(path)?.dir) return cb(fail("ENOTDIR"));
      const prefix = path === "/" ? "/" : path + "/";
      const names = [];
      for (const k of nodes.keys()) {
        if (k !== path && k.startsWith(prefix) && !k.slice(prefix.length).includes("/")) names.push(k.slice(prefix.length));
      }
      cb(null, names);
    },
    unlink(path, cb) {
      path = norm(path);
      const n = nodes.get(path);
      if (!n) return cb(fail("ENOENT"));
      if (n.dir) return cb(fail("EISDIR"));
      n.data.fill(0); // the bytes of a document do not linger
      nodes.delete(path);
      cb(null);
    },
    rmdir(path, cb) {
      path = norm(path);
      if (!nodes.has(path)) return cb(fail("ENOENT"));
      for (const k of nodes.keys()) if (k.startsWith(path + "/")) return cb(fail("ENOTEMPTY"));
      nodes.delete(path);
      cb(null);
    },
    rename(from, to, cb) {
      from = norm(from); to = norm(to);
      const n = nodes.get(from);
      if (!n) return cb(fail("ENOENT"));
      nodes.delete(from);
      nodes.set(to, n);
      cb(null);
    },
    ftruncate(fd, size, cb) {
      const f = fds.get(fd);
      if (!f) return cb(fail("EBADF"));
      grow(f.node, size);
      f.node.size = size;
      cb(null);
    },
    truncate(path, size, cb) {
      const n = nodes.get(norm(path));
      if (!n) return cb(fail("ENOENT"));
      grow(n, size);
      n.size = size;
      cb(null);
    },
    fsync(fd, cb) { cb(null); },
    chmod(path, mode, cb) { cb(null); },
    fchmod(fd, mode, cb) { cb(null); },
    chown(path, uid, gid, cb) { cb(null); },
    fchown(fd, uid, gid, cb) { cb(null); },
    lchown(path, uid, gid, cb) { cb(null); },
    utimes(path, atime, mtime, cb) { cb(null); },
    link(path, link, cb) { cb(fail("ENOSYS")); },
    symlink(path, link, cb) { cb(fail("ENOSYS")); },
    readlink(path, cb) { cb(fail("EINVAL")); },
  };
})();
