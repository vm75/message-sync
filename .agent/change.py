from pathlib import Path
import subprocess

root = Path('.')
tracker = root / 'docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md'
text = tracker.read_text()
for issue in range(102, 109):
    marker = f'- [x] #{issue} '
    if marker not in text:
        raise SystemExit(f'tracker is not complete for #{issue}')

# Validate the shipped browser code before removing the temporary test harness.
for script in [
    'internal/api/web/js/api.js',
    'internal/api/web/js/app-base.js',
    'internal/api/web/js/app.js',
]:
    subprocess.run(['node', '--check', script], check=True)

# All tracked issues are closed; remove temporary implementation bookkeeping and branch-only CI.
for path in [
    'docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md',
    '.github/workflows/telegram-mtproto-branch-ci.yml',
    '.github/workflows/agent-patch-runner.yml',
]:
    p = root / path
    if not p.exists():
        raise SystemExit(f'missing cleanup target: {path}')
    p.unlink()
