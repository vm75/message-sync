from pathlib import Path
full=Path('.agent/change_full.py')
s=full.read_text()
s=s.replace('AccessHash: 9}}}\n        a.authorized = true', 'AccessHash: 9}}}}\n        a.authorized = true')
s=s.replace('AccessHash: 9}}}\n    auth.authorized = true', 'AccessHash: 9}}}}\n    auth.authorized = true')
full.unlink()
exec(compile(s, '.agent/change_full.py', 'exec'))
