#!/usr/bin/env python3
"""Host-rewriting proxy in front of the compose `gcs` (fake-gcs-server).

The emulator runs with `-public-host=gcs:4443`, which is the name the *api
container* resolves. A process on the host has to reach it as
`localhost:4443`, and fake-gcs-server answers the XML object API by matching
the request's Host against its public host — so a host-side API configured
with `STORAGE_EMULATOR_HOST=http://localhost:4443` gets a 404 on every LFS
object read while the JSON API appears to work.

This proxy forwards everything to the emulator with `Host: gcs:4443`, so
`STORAGE_EMULATOR_HOST=http://localhost:14443` behaves exactly like it does
inside the compose network. Standard library only, on purpose: it has to run
without touching whatever Python environment happens to be active.

    scripts/gcs-host-proxy.py [--port 14443] [--upstream localhost:4443]
                              [--public-host gcs:4443]
"""

from __future__ import annotations

import argparse
import http.client
import shutil
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# Hop-by-hop headers must not be forwarded (RFC 7230 §6.1); passing Connection
# or Transfer-Encoding through would desync the two sides' framing.
HOP_BY_HOP = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}

# Big enough that a GB-scale object is not thousands of tiny writes, small
# enough that memory stays flat no matter how large the object is.
COPY_CHUNK = 256 * 1024


class _LimitedReader:
    """Exactly `remaining` bytes of `stream`, as a file-like http.client can send."""

    def __init__(self, stream, remaining: int) -> None:
        self._stream = stream
        self._remaining = remaining

    def read(self, size: int = -1) -> bytes:
        if self._remaining <= 0:
            return b""
        want = (
            self._remaining if size is None or size < 0 else min(size, self._remaining)
        )
        chunk = self._stream.read(want)
        self._remaining -= len(chunk)
        return chunk


class _ChunkedReader:
    """Decodes a chunked request body lazily, so http.client can re-chunk it."""

    def __init__(self, stream) -> None:
        self._stream = stream
        self._buf = b""
        self._done = False

    def read(self, size: int = -1) -> bytes:
        while not self._done and (size is None or size < 0 or len(self._buf) < size):
            line = self._stream.readline().strip()
            chunk_size = int(line.split(b";")[0] or b"0", 16)
            if chunk_size == 0:
                self._stream.readline()  # trailing CRLF
                self._done = True
                break
            self._buf += self._stream.read(chunk_size)
            self._stream.readline()  # CRLF after the chunk
        if size is None or size < 0:
            out, self._buf = self._buf, b""
        else:
            out, self._buf = self._buf[:size], self._buf[size:]
        return out


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    upstream = "localhost:4443"
    public_host = "gcs:4443"

    def log_message(self, fmt: str, *args) -> None:  # noqa: A002 - stdlib signature
        # One line per request on stderr, so the proxy can share a terminal
        # with the API it is serving without drowning it.
        sys.stderr.write("gcs-proxy %s\n" % (fmt % args))

    def _forward(self) -> None:
        # Both directions stream. The API proxies LFS objects through here in
        # emulator mode, so a multi-GB checkpoint must not have to fit in this
        # process's memory — and buffering would also break range reads, which
        # the parquet viewer leans on for row-group fetches.
        body = None
        length = self.headers.get("Content-Length")
        if length:
            body = _LimitedReader(self.rfile, int(length))
        elif self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            body = _ChunkedReader(self.rfile)

        headers = {
            k: v
            for k, v in self.headers.items()
            if k.lower() not in HOP_BY_HOP and k.lower() != "host"
        }
        headers["Host"] = self.public_host
        # http.client streams a file-like body: with Content-Length it sends
        # exactly that many bytes, without one it re-chunks as it reads.

        conn = http.client.HTTPConnection(self.upstream, timeout=300)
        try:
            try:
                conn.request(self.command, self.path, body=body, headers=headers)
                resp = conn.getresponse()
            except OSError as err:
                self.send_error(502, "upstream %s: %s" % (self.upstream, err))
                return

            self.send_response(resp.status, resp.reason)
            for k, v in resp.getheaders():
                if k.lower() in HOP_BY_HOP:
                    continue
                self.send_header(k, v)
            # Content-Length is passed through untouched above. When upstream
            # answered without one (it chunked its reply, and Transfer-Encoding
            # is hop-by-hop so it is not forwarded), the only framing left is
            # closing the connection.
            if resp.getheader("Content-Length") is None and self.command != "HEAD":
                self.send_header("Connection", "close")
                self.close_connection = True
            self.end_headers()

            if self.command != "HEAD":
                try:
                    shutil.copyfileobj(resp, self.wfile, COPY_CHUNK)
                except OSError as err:
                    # Headers are already out, so there is no status left to
                    # send: log it and drop the connection.
                    self.log_message("stream aborted: %s", err)
                    self.close_connection = True
        finally:
            conn.close()

    do_GET = do_PUT = do_POST = do_HEAD = do_DELETE = do_PATCH = do_OPTIONS = _forward


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--port", type=int, default=14443, help="port to listen on (default: 14443)"
    )
    ap.add_argument(
        "--upstream",
        default="localhost:4443",
        help="published fake-gcs address (default: localhost:4443)",
    )
    ap.add_argument(
        "--public-host",
        default="gcs:4443",
        help="value to send as Host, matching the emulator's -public-host (default: gcs:4443)",
    )
    args = ap.parse_args()

    Handler.upstream = args.upstream
    Handler.public_host = args.public_host
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(
        f"gcs-host-proxy: http://localhost:{args.port} -> {args.upstream} (Host: {args.public_host})",
        file=sys.stderr,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
