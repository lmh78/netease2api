"""Embed the corresponding source without local state or built artifacts."""
from pathlib import Path
import zipfile

root = Path(__file__).resolve().parents[1]
target = root / "internal/gateway/web/source.zip"
(root / "internal/gateway/web/LICENSE.txt").write_bytes((root / "LICENSE").read_bytes())
extensions = {".go", ".js", ".css", ".html", ".md", ".py", ".ps1", ".yaml", ".yml"}
special = {"LICENSE", "go.mod", "go.sum", "Dockerfile", ".gitignore", ".dockerignore", "README.md", "PROTOCOL_SOURCE.md", "main.go", "url.go", "compose.yaml", "run.ps1"}
with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as archive:
    for path in sorted(root.rglob("*")):
        relative = path.relative_to(root)
        if not path.is_file() or any(part in {"data", "dist", "release", ".git", ".venv", "__pycache__"} for part in relative.parts):
            continue
        if path.name.endswith((".local.json", ".log")):
            continue
        fixture = path.suffix == ".json" and "testdata" in relative.parts
        public = (len(relative.parts) == 1 and path.name in special) or (relative.parts[0] in {"internal", "scripts"} and (path.suffix in extensions or fixture)) or relative == Path("internal/gateway/web/LICENSE.txt")
        if public:
            info = zipfile.ZipInfo("netease2api/" + relative.as_posix(), (1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o644 << 16
            archive.writestr(info, path.read_bytes())
print("Embedded source archive updated; private data excluded.")
