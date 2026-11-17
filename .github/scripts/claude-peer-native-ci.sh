#!/usr/bin/env bash
set -euo pipefail

if [[ "${GITHUB_ACTIONS:-}" != true || "${RUNNER_OS:-}" != Linux || "${RUNNER_ARCH:-}" != X64 ]]; then
  echo 'This real-CLI / simulated-model fixture runs only on a GitHub Linux x64 runner.' >&2
  exit 1
fi

fixture_stage="$(mktemp -d "${RUNNER_TEMP:?}/ym-claude-peer.XXXXXXXX")"
cleanup() {
  if [[ -s "$fixture_stage/container.cid" ]]; then
    fixture_container="$(cat "$fixture_stage/container.cid")"
    if [[ "$fixture_container" =~ ^[a-f0-9]{64}$ ]]; then docker rm -f "$fixture_container" >/dev/null 2>&1 || true; fi
  fi
  rm -rf "$fixture_stage"
}
trap cleanup EXIT INT TERM
chmod 755 "$fixture_stage"

# The official native artifact is downloaded before the isolated runtime starts.
python3 - "$fixture_stage" <<'PY'
import base64, hashlib, pathlib, shutil, sys, tarfile, urllib.request
stage = pathlib.Path(sys.argv[1])
url = 'https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/-/claude-code-linux-x64-2.1.314.tgz'
expected = 'Yg0a5mpdkGClK0rgFMxbCWZW3wZtqgOhYjbISALqyL5tfADHhX9Ms6ExA0/effgSJ9MaWfHMPntryeOG6Mk6HQ=='
archive = stage / 'claude.tgz'
digest = hashlib.sha512()
size = 0
with urllib.request.urlopen(url, timeout=60) as response, archive.open('wb') as output:
    while chunk := response.read(1024 * 1024):
        size += len(chunk)
        if size > 256 * 1024 * 1024:
            raise SystemExit('Pinned native archive exceeds size limit')
        digest.update(chunk)
        output.write(chunk)
if base64.b64encode(digest.digest()).decode() != expected:
    raise SystemExit('Pinned Claude 2.1.314 native artifact integrity mismatch')
with tarfile.open(archive, 'r:gz') as package:
    binaries = [entry for entry in package.getmembers() if entry.isfile() and pathlib.PurePosixPath(entry.name).name in ('claude', 'claude.exe')]
    if len(binaries) != 1 or not 1024 * 1024 <= binaries[0].size <= 400 * 1024 * 1024:
        raise SystemExit('Pinned package has an unexpected native binary layout')
    with package.extractfile(binaries[0]) as source, (stage / 'claude').open('wb') as output:
        shutil.copyfileobj(source, output, 1024 * 1024)
(stage / 'claude').chmod(0o555)
archive.unlink()
PY

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -p 1 -o "$fixture_stage/peer.test" ./internal/harness/claude
chmod 555 "$fixture_stage/peer.test"
docker pull node:22-bookworm-slim
fixture_image="$(docker image inspect --format '{{.Id}}' node:22-bookworm-slim)"
timeout --signal=TERM --kill-after=10s 180s docker run --rm --cidfile "$fixture_stage/container.cid" \
  --network none --user node:node --cpus 1 --memory 1536m --pids-limit 96 \
  --read-only --tmpfs /tmp:rw,nosuid,nodev,size=512m --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=bind,src=$fixture_stage,dst=/fixture,readonly" --workdir /tmp \
  --env HOME=/tmp/fixture-home --env TMPDIR=/tmp --env GOMAXPROCS=1 --env GITHUB_ACTIONS=true \
  --env YM_REAL_CLAUDE_PEER_FIXTURE=1 --env YM_REAL_CLAUDE_BIN=/fixture/claude \
  "$fixture_image" /fixture/peer.test -test.run '^TestRealClaudePeerTransportWithSimulatedModel$' -test.v -test.timeout=150s
