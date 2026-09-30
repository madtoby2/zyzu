"""Conservative VPS cache recovery. Dry-run unless --apply is supplied."""
import argparse
import fcntl
import os
from pathlib import Path
import shutil
import time


def in_use(nodes):
    identities = {(p.stat().st_dev, p.stat().st_ino) for p in nodes}
    for proc in Path('/proc').iterdir():
        if not proc.name.isdigit():
            continue
        try:
            handles = [proc / 'cwd', * (proc / 'fd').iterdir()]
        except FileNotFoundError:
            continue
        for handle in handles:
            try:
                st = handle.stat()
                if (st.st_dev, st.st_ino) in identities:
                    return True
            except FileNotFoundError:
                continue
    return False


def eligible(path, root, age):
    if path.is_symlink() or path.resolve().parent != root.resolve():
        return None
    nodes = [path, *path.rglob('*')] if path.is_dir() else [path]
    cutoff = time.time() - age
    for node in nodes:
        if node.is_symlink():
            return None
        st = node.stat()
        if max(st.st_mtime, st.st_ctime) > cutoff:
            return None
    if in_use(nodes):
        return None
    return sum(p.stat().st_size for p in nodes if p.is_file())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    with open('/run/zyzu-cache-janitor.lock', 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        total = 0
        for root, age in [(Path('/opt/zyzu/videos'), 48 * 3600),
                          (Path('/opt/deploy/data/tmp/game_pipeline_download/games'), 7 * 86400)]:
            if not root.exists() or root.is_symlink():
                continue
            for path in root.iterdir():
                if root.name == 'videos' and (not path.is_file() or not path.name.endswith('.mp4')):
                    continue
                size = eligible(path, root, age)
                if size is None:
                    continue
                print(f'{"REMOVE" if args.apply else "CANDIDATE"} bytes={size} path={path}', flush=True)
                if args.apply:
                    # Recheck immediately before deletion; recent or open jobs are retained.
                    if eligible(path, root, age) is None:
                        continue
                    if path.is_dir():
                        shutil.rmtree(path)
                    else:
                        path.unlink()
                total += size
        free = shutil.disk_usage('/').free
        level = 'CRITICAL' if free < 5 * 2**30 else 'WARNING' if free < 10 * 2**30 else 'INFO'
        print(f'{level} apply={args.apply} reclaimed_bytes={total} free_bytes={free}', flush=True)


if __name__ == '__main__':
    main()
