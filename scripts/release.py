"""Build clean release assets. Only explicit binaries, docs, and source are packaged."""
import argparse
import hashlib
import io
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import zipfile

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--version", default="v0.3.0")
args = parser.parse_args()
if not re.fullmatch(r"v\d+\.\d+\.\d+", args.version):
    raise SystemExit("Version must be vMAJOR.MINOR.PATCH")
version = args.version
subprocess.run([sys.executable, str(root / "scripts/source_archive.py")], cwd=root, check=True)
dist = root / "dist"
output = root / "release"
dist.mkdir(exist_ok=True)
output.mkdir(exist_ok=True)
for system, filename in [("windows", "netease2api-windows-amd64.exe"), ("linux", "netease2api-linux-amd64")]:
    settings = dict(os.environ, GOOS=system, GOARCH="amd64", CGO_ENABLED="0")
    subprocess.run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(dist / filename), "."], cwd=root, env=settings, check=True)

docs = ["README.md", "LICENSE", "PROTOCOL_SOURCE.md"]
windows = output / f"netease2api-{version}-windows-amd64.zip"
with zipfile.ZipFile(windows, "w", zipfile.ZIP_DEFLATED) as archive:
    archive.write(dist / "netease2api-windows-amd64.exe", "netease2api/netease2api.exe")
    for name in docs + ["run.ps1"]:
        archive.write(root / name, "netease2api/" + name)
linux = output / f"netease2api-{version}-linux-amd64.tar.gz"
with tarfile.open(linux, "w:gz") as archive:
    for path, name, mode in [(dist / "netease2api-linux-amd64", "netease2api", 0o755)] + [(root / name, name, 0o644) for name in docs]:
        data = path.read_bytes()
        info = tarfile.TarInfo("netease2api/" + name)
        info.size, info.mode = len(data), mode
        archive.addfile(info, io.BytesIO(data))
source = output / f"netease2api-{version}-source.zip"
source.write_bytes((root / "internal/gateway/web/source.zip").read_bytes())
assets = [windows, linux, source]
checksums = "".join(hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.name + "\n" for path in assets)
(output / "SHA256SUMS.txt").write_text(checksums, encoding="ascii")
print("Built Windows, Linux, source, and SHA-256 release assets; no runtime state included.")
