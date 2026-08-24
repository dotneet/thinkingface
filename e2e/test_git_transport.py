"""Writing to a repository with the plain `git` binary.

The rest of the suite writes exclusively through the HF REST commit API
(`upload_file` / `create_commit`), and the only git it exercises is `clone` /
`pull` -- i.e. `git-upload-pack`. This file covers the other direction:
`git-receive-pack`, over both transports the server speaks.

  * git smart HTTP  (backend/internal/gitserver, mounted in api/server.go at
    `/{ns}/{name}` for models and `/datasets/{ns}/{name}` for datasets)
  * git over SSH    (backend/internal/sshserver, design doc §5), including
    registering the key through /api/v1/me/ssh-keys the way the web UI's
    /settings/ssh-keys page does

Compatibility with the `git` CLI is one of the four clients named in the
design doc's §1 (and in CLAUDE.md's invariant 5), so a push regression --
post-receive indexing, LFS pointer handling, ref updates -- belongs here
rather than only in Go unit tests.

Requires a running server; see e2e/README.md. The SSH tests additionally
require the SSH listener (TF_SSH_ENABLED); they skip when nothing answers on
its port, since that is a valid deployment rather than a regression.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import urllib.parse
import warnings
from pathlib import Path

import pytest
import requests
from huggingface_hub import HfApi, hf_hub_download

# A push needs an author, and CI containers have no git identity configured.
# Passing it in the environment keeps it local to these tests -- nothing here
# writes to the developer's ~/.gitconfig.
_GIT_IDENTITY = {
    "GIT_AUTHOR_NAME": "thinkingface e2e",
    "GIT_AUTHOR_EMAIL": "e2e@thinkingface.invalid",
    "GIT_COMMITTER_NAME": "thinkingface e2e",
    "GIT_COMMITTER_EMAIL": "e2e@thinkingface.invalid",
    # Never let git block on a credential prompt: an auth failure has to
    # surface as a non-zero exit, not as a test that hangs until the timeout.
    "GIT_TERMINAL_PROMPT": "0",
    "GIT_ASKPASS": "",
    "GIT_CONFIG_NOSYSTEM": "1",
}


def _git_env(**extra: str) -> dict[str, str]:
    return {**os.environ, **_GIT_IDENTITY, **extra}


def _git(*args: str, env: dict[str, str] | None = None, timeout: int = 60) -> str:
    """Run git, and on failure raise with the captured stderr attached.

    `check=True` alone reports only "exit status 128", which for a push is
    never enough to tell an auth failure from a rejected ref.
    """
    proc = subprocess.run(
        ["git", *args],
        capture_output=True,
        text=True,
        env=env or _git_env(),
        timeout=timeout,
    )
    if proc.returncode != 0:
        raise AssertionError(
            f"git {' '.join(args)} failed ({proc.returncode})\n"
            f"--- stdout ---\n{proc.stdout}\n--- stderr ---\n{proc.stderr}"
        )
    return proc.stdout


def _http_remote(hf_endpoint: str, path: str, *, namespace: str, token: str) -> str:
    """An authenticated git remote URL, e.g. http://user:token@host/ns/name.git.

    Credentials in the URL rather than a credential helper: the helper would
    write into the developer's keychain, and the token is minted per session
    by conftest anyway.
    """
    parsed = urllib.parse.urlsplit(hf_endpoint)
    netloc = f"{urllib.parse.quote(namespace, safe='')}:{urllib.parse.quote(token, safe='')}@{parsed.netloc}"
    return urllib.parse.urlunsplit((parsed.scheme, netloc, path, "", ""))


def _commit_file(clone: Path, relpath: str, content: bytes, message: str) -> None:
    target = clone / relpath
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(content)
    _git("-C", str(clone), "add", "--", relpath)
    _git("-C", str(clone), "commit", "-m", message)


def _current_branch(clone: Path) -> str:
    return _git("-C", str(clone), "rev-parse", "--abbrev-ref", "HEAD").strip()


# ------------------------------------------------------------ smart HTTP


def test_git_push_over_smart_http_to_a_model(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str, tmp_path
) -> None:
    """clone -> commit -> push, then the pushed bytes come back out of the
    REST surface. That last part is the point: a push has to run the same
    post-receive indexing (syncer) as a REST commit, or the file lands in the
    git repo and is invisible to `huggingface_hub`."""
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"# seeded over REST\n",
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add README",
        )

        remote = _http_remote(
            hf_endpoint, f"/{namespace}/{unique_name}.git", namespace=namespace, token=hf_token
        )
        clone = tmp_path / "clone"
        _git("clone", remote, str(clone))

        payload = b"pushed with the git binary\n"
        _commit_file(clone, "notes/from-git.txt", payload, "Add a file over git push")
        _git("-C", str(clone), "push", "origin", _current_branch(clone))

        # The commit is on the server's branch...
        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="model")
        assert "notes/from-git.txt" in files
        assert "README.md" in files

        # ...the metadata index caught up (this is the syncer's job)...
        commits = hf_api.list_repo_commits(repo_id=repo_id, repo_type="model")
        assert commits[0].title == "Add a file over git push"

        # ...and resolve serves the exact bytes.
        downloaded = hf_hub_download(
            repo_id=repo_id, repo_type="model", filename="notes/from-git.txt"
        )
        assert Path(downloaded).read_bytes() == payload
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_git_push_over_smart_http_to_a_dataset(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str, tmp_path
) -> None:
    """Datasets answer on the `/datasets/` prefix, models at the root. The
    prefix is a separate chi route (api/server.go's mountRepoTransport), so
    the model test above says nothing about this one."""
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"# seeded over REST\n",
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Add README",
        )

        remote = _http_remote(
            hf_endpoint,
            f"/datasets/{namespace}/{unique_name}.git",
            namespace=namespace,
            token=hf_token,
        )
        clone = tmp_path / "clone"
        _git("clone", remote, str(clone))
        assert (clone / "README.md").read_bytes() == b"# seeded over REST\n"

        _commit_file(clone, "data/rows.csv", b"id,text\n1,hello\n", "Add rows.csv over git push")
        _git("-C", str(clone), "push", "origin", _current_branch(clone))

        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="dataset")
        assert "data/rows.csv" in files
        downloaded = hf_hub_download(repo_id=repo_id, repo_type="dataset", filename="data/rows.csv")
        assert Path(downloaded).read_bytes() == b"id,text\n1,hello\n"
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


@pytest.mark.skipif(shutil.which("git-lfs") is None, reason="git-lfs is not installed")
def test_git_push_with_an_lfs_file_over_smart_http(
    hf_api: HfApi, hf_endpoint: str, hf_token: str, namespace: str, unique_name: str, tmp_path
) -> None:
    """A push that carries an LFS object, which is a different protocol on top
    of the same transport: git-lfs runs its own batch/upload round trip
    against /info/lfs/objects/batch before `git push` sends the pointer.

    `*.bin` is LFS-tracked by the .gitattributes the server seeds at repo
    creation (design doc §3), so the clone below picks the filter up without
    the test writing any .gitattributes of its own -- which also means this
    test fails if that seeding ever regresses.
    """
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        remote = _http_remote(
            hf_endpoint, f"/{namespace}/{unique_name}.git", namespace=namespace, token=hf_token
        )
        clone = tmp_path / "clone"
        _git("clone", remote, str(clone))

        gitattributes = (clone / ".gitattributes").read_text()
        assert "*.bin" in gitattributes, (
            f"the server did not seed .gitattributes with an *.bin rule; got:\n{gitattributes}"
        )

        # --local, not the default global install: this must not touch the
        # developer's ~/.gitconfig.
        _git("-C", str(clone), "lfs", "install", "--local")

        # Not compressible and not tiny, so a pointer file cannot be confused
        # with the payload itself.
        payload = bytes(range(256)) * 512  # 128 KiB
        _commit_file(clone, "weights.bin", payload, "Add weights.bin over git push")

        # The working tree holds the real bytes; what is committed is the
        # pointer. Confirm the filter actually engaged, otherwise the rest of
        # this test would be a plain-file push wearing an LFS costume.
        staged = _git("-C", str(clone), "show", "HEAD:weights.bin")
        assert staged.startswith("version https://git-lfs.github.com/spec/v1")

        _git("-C", str(clone), "push", "origin", _current_branch(clone), timeout=120)

        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="model")
        assert "weights.bin" in files

        # The tree reports it as an LFS entry with the real size, not the
        # ~130-byte pointer.
        entry = next(
            e
            for e in hf_api.list_repo_tree(repo_id=repo_id, repo_type="model")
            if getattr(e, "path", None) == "weights.bin"
        )
        assert getattr(entry, "lfs", None) is not None, f"weights.bin is not LFS-backed: {entry!r}"
        assert entry.size == len(payload)

        # And resolve hands back the object, not the pointer.
        downloaded = hf_hub_download(repo_id=repo_id, repo_type="model", filename="weights.bin")
        assert Path(downloaded).read_bytes() == payload
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


# ------------------------------------------------------------------- SSH


@pytest.fixture()
def registered_ssh_key(hf_endpoint: str, hf_token: str, tmp_path):
    """Generate a throwaway ed25519 key, register it at /api/v1/me/ssh-keys,
    and yield a GIT_SSH_COMMAND that uses only that key.

    The key is deleted again in the finalizer: without it every run would
    leave a live push credential on the admin account, the same reason
    conftest revokes the token it mints.
    """
    if shutil.which("ssh-keygen") is None or shutil.which("ssh") is None:
        pytest.skip("ssh / ssh-keygen are not installed")

    key_path = tmp_path / "id_ed25519"
    subprocess.run(
        ["ssh-keygen", "-t", "ed25519", "-N", "", "-C", "thinkingface-e2e", "-f", str(key_path)],
        check=True,
        capture_output=True,
        timeout=30,
    )
    public_key = (tmp_path / "id_ed25519.pub").read_text().strip()

    headers = {"Authorization": f"Bearer {hf_token}"}
    resp = requests.post(
        f"{hf_endpoint}/api/v1/me/ssh-keys",
        headers=headers,
        json={"title": "thinkingface e2e", "key": public_key},
        timeout=10,
    )
    assert resp.status_code == 200, (
        f"registering the ssh key failed: {resp.status_code} {resp.text}"
    )
    key_id = resp.json()["id"]

    # StrictHostKeyChecking=no + UserKnownHostsFile=/dev/null: the server mints
    # its host key on first start, so there is nothing to pin, and writing to
    # the developer's ~/.ssh/known_hosts would be a side effect of running the
    # tests. IdentitiesOnly stops the agent from offering unrelated keys --
    # without it a developer's own registered key could authenticate and this
    # test would pass while proving nothing about the key it just registered.
    ssh_command = (
        f"ssh -i {key_path} -o IdentitiesOnly=yes -o StrictHostKeyChecking=no "
        "-o UserKnownHostsFile=/dev/null -o BatchMode=yes -o LogLevel=ERROR"
    )
    try:
        yield ssh_command
    finally:
        # Best-effort, like the token revocation in conftest: a cleanup that
        # raises stacks a teardown ERROR on top of whatever the test actually
        # reported, which buries the result that matters. A key left behind is
        # tidiness, not correctness.
        try:
            requests.delete(
                f"{hf_endpoint}/api/v1/me/ssh-keys/{key_id}", headers=headers, timeout=10
            )
        except requests.RequestException as exc:
            warnings.warn(f"could not delete the e2e ssh key {key_id}: {exc}", stacklevel=1)


def test_git_clone_and_push_over_ssh(
    hf_api: HfApi,
    namespace: str,
    unique_name: str,
    ssh_endpoint: tuple[str, int],
    registered_ssh_key: str,
    tmp_path,
) -> None:
    """Register a key, clone over ssh://, push, and read the result back over
    REST. Public key auth only -- there is no password path -- so this also
    covers the sshserver's key lookup."""
    host, port = ssh_endpoint
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"# seeded over REST\n",
            path_in_repo="README.md",
            repo_id=repo_id,
            repo_type="model",
            commit_message="Add README",
        )

        env = _git_env(GIT_SSH_COMMAND=registered_ssh_key)
        remote = f"ssh://git@{host}:{port}/{namespace}/{unique_name}.git"

        clone = tmp_path / "clone"
        _git("clone", remote, str(clone), env=env)
        assert (clone / "README.md").read_bytes() == b"# seeded over REST\n"

        payload = b"pushed over ssh\n"
        _commit_file(clone, "notes/over-ssh.txt", payload, "Add a file over ssh push")
        _git("-C", str(clone), "push", "origin", _current_branch(clone), env=env)

        files = hf_api.list_repo_files(repo_id=repo_id, repo_type="model")
        assert "notes/over-ssh.txt" in files
        downloaded = hf_hub_download(
            repo_id=repo_id, repo_type="model", filename="notes/over-ssh.txt"
        )
        assert Path(downloaded).read_bytes() == payload
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")


def test_git_clone_over_ssh_for_a_dataset(
    hf_api: HfApi,
    namespace: str,
    unique_name: str,
    ssh_endpoint: tuple[str, int],
    registered_ssh_key: str,
    tmp_path,
) -> None:
    """Over SSH the repo kind is a path prefix the server parses itself
    (sshserver.ParseCommand accepts `ns/name`, `models/...` and
    `datasets/...`), so the dataset form needs its own case."""
    host, port = ssh_endpoint
    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="dataset", private=False)
    try:
        hf_api.upload_file(
            path_or_fileobj=b"id,text\n1,hello\n",
            path_in_repo="data/rows.csv",
            repo_id=repo_id,
            repo_type="dataset",
            commit_message="Add rows.csv",
        )

        env = _git_env(GIT_SSH_COMMAND=registered_ssh_key)
        remote = f"ssh://git@{host}:{port}/datasets/{namespace}/{unique_name}.git"

        clone = tmp_path / "clone"
        _git("clone", remote, str(clone), env=env)
        assert (clone / "data" / "rows.csv").read_bytes() == b"id,text\n1,hello\n"

        _commit_file(clone, "data/more.csv", b"id,text\n2,world\n", "Add more.csv over ssh")
        _git("-C", str(clone), "push", "origin", _current_branch(clone), env=env)

        assert "data/more.csv" in hf_api.list_repo_files(repo_id=repo_id, repo_type="dataset")
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="dataset")


def test_ssh_push_is_rejected_without_a_registered_key(
    hf_api: HfApi, namespace: str, unique_name: str, ssh_endpoint: tuple[str, int], tmp_path
) -> None:
    """An unregistered key must not get in. Without this, the tests above
    would still pass on a server that authenticated everybody."""
    if shutil.which("ssh-keygen") is None or shutil.which("ssh") is None:
        pytest.skip("ssh / ssh-keygen are not installed")

    host, port = ssh_endpoint
    key_path = tmp_path / "stranger_ed25519"
    subprocess.run(
        ["ssh-keygen", "-t", "ed25519", "-N", "", "-C", "stranger", "-f", str(key_path)],
        check=True,
        capture_output=True,
        timeout=30,
    )

    repo_id = f"{namespace}/{unique_name}"
    hf_api.create_repo(repo_id=repo_id, repo_type="model", private=False)
    try:
        env = _git_env(
            GIT_SSH_COMMAND=(
                f"ssh -i {key_path} -o IdentitiesOnly=yes -o StrictHostKeyChecking=no "
                "-o UserKnownHostsFile=/dev/null -o BatchMode=yes -o LogLevel=ERROR"
            )
        )
        proc = subprocess.run(
            [
                "git",
                "clone",
                f"ssh://git@{host}:{port}/{namespace}/{unique_name}.git",
                str(tmp_path / "nope"),
            ],
            capture_output=True,
            text=True,
            env=env,
            timeout=60,
        )
        assert proc.returncode != 0, "an unregistered ssh key was allowed to clone"
        # A non-zero exit alone would also be satisfied by a server that fails
        # every SSH request for an unrelated reason -- a broken path parser,
        # say -- and this case would then keep reporting that authentication
        # works. Insist the refusal is specifically about the key.
        stderr = proc.stderr.lower()
        assert "publickey" in stderr or "permission denied" in stderr, (
            "the clone failed, but not because the key was unregistered; "
            f"stderr was: {proc.stderr.strip()!r}"
        )
    finally:
        hf_api.delete_repo(repo_id=repo_id, repo_type="model")
