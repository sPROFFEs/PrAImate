#!/usr/bin/env python3
"""Checksum only the staged release files, never a previous checksum manifest."""
import hashlib
from pathlib import Path
root=Path.cwd()
lines=[]
for file in sorted(root.iterdir()):
    if file.is_file() and file.name!='SHA256SUMS':
        digest=hashlib.sha256()
        with file.open('rb') as stream:
            for block in iter(lambda:stream.read(1024*1024),b''):digest.update(block)
        lines.append(digest.hexdigest()+'  '+file.name+'\n')
if not lines:raise SystemExit('No release artifacts to checksum')
(root/'SHA256SUMS').write_text(''.join(lines))
