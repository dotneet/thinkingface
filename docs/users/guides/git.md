# Working with Git

Every repository on a thinkingface instance is a real git repository — a bare repository on
the server, with Git LFS for large files. Nothing about it is special: `git clone`,
`git push`, branches and tags all behave the way you expect. This page covers the URLs,
credentials, LFS setup and the few limits worth knowing about.

## Clone over HTTP

Datasets carry a `/datasets` prefix in their URL; models sit at the root:

```bash
git clone http://localhost:8080/datasets/admin/imdb-reviews.git
git clone http://localhost:8080/admin/sentiment-base.git
```

Models also answer at an explicit `/models/{namespace}/{name}.git`, which is useful when a
tool wants the repository type to be unambiguous. The `.git` suffix is optional everywhere.

Reads are open — cloning needs no credentials at all. Pushing does.

### Credentials

Authentication over HTTP is Basic auth, with your access token as the **password**. The
username is ignored for a token, so anything works there; use your account name to keep it
readable.

```bash
git config --global credential.helper store
git push origin main
```

The first push prompts for a username and a password: enter your account name (`admin`) and
the token (`tf_xxxxxxxxxxxx`). With a credential helper configured, git stores the answer and
stops asking. You can also put the token straight into the remote URL, but that writes it
into `.git/config` in plain text, so prefer the helper.

Your account password is accepted in place of a token, which is what makes a first push work
before you have issued anything. A token is still the better credential: it can be scoped to
`read`, it can be revoked on its own, and it never exposes the password you log in with. See
[Authentication](../reference/authentication.md).

## Git LFS

Large files move over Git LFS. Install it once per machine:

```bash
git lfs install
```

### On clone

Cloning a repository that contains LFS files downloads the real bytes as part of the
checkout, so the working tree holds actual files, not pointer text. In a production
deployment the objects come from a time-limited signed URL and travel directly between your
machine and the object store — the API server is not in the data path.

If you want the clone to skip that and fetch objects later:

```bash
GIT_LFS_SKIP_SMUDGE=1 git clone http://localhost:8080/datasets/admin/imdb-reviews.git
cd imdb-ja
git lfs pull
```

### On push

Which files go to LFS is decided by `.gitattributes`. Every repository is created with a
default set of patterns covering the formats that are always large — `*.safetensors`,
`*.parquet`, `*.bin`, `*.gguf`, `*.ckpt`, `*.onnx`, `*.zip` and around thirty more — so a
freshly cloned repository already tracks the right things.

To add a pattern, use `git lfs track` and commit the result:

```bash
git lfs track "*.jsonl"
git add .gitattributes data/train.jsonl
git commit -m "Track jsonl files with LFS"
git push origin main
```

!!! warning "`.gitattributes` must be committed before the files it covers"

    git-lfs reads the working tree's `.gitattributes` when a file is staged. Committing a
    large file first and adding the pattern afterwards leaves the file in git history as an
    ordinary blob, and fixing that means rewriting history.

Note the asymmetry with the other upload routes: over git, your **local** git-lfs decides
what becomes an LFS object. When you upload through `huggingface_hub`, the `hf` CLI or `tf`,
the **server** decides, from the repository's `.gitattributes` plus a 10 MiB size threshold.
[Uploading Files](uploading.md#how-files-are-routed-to-git-lfs) has the full rules.

## Clone over SSH

Git over SSH is available when the server runs with `TF_SSH_ENABLED=true`. The
`docker compose` stack enables it and publishes port 2222; a deployment you build yourself
has it off by default.

### Register a public key

SSH authenticates by public key only — there is no password prompt to fall back to. Generate
a key if you do not have one, then register the **public** half in the web UI under
**Settings → SSH keys** (<http://localhost:3000/settings/ssh-keys>):

```bash
ssh-keygen -t ed25519 -C "you@example.com"
cat ~/.ssh/id_ed25519.pub
```

Paste the single line from the `.pub` file. What the server accepts:

| Key type | Accepted |
|---|---|
| `ssh-ed25519` | Yes |
| `ecdsa-sha2-nistp256` / `-nistp384` / `-nistp521` | Yes |
| Security-key variants (`sk-ssh-ed25519@openssh.com`, `sk-ecdsa-sha2-nistp256@openssh.com`) | Yes |
| `ssh-rsa` | Yes, at 2048 bits or more |
| `ssh-dss` (DSA) | No |

The key must be one line with no `authorized_keys` options, and a fingerprint may only be
registered once across the whole instance — the account is identified by the key alone, so
the same key cannot be shared between two accounts. Pasting a private key by mistake is
detected and refused rather than stored.

### Clone

```bash
git clone ssh://git@localhost:2222/datasets/admin/imdb-reviews.git
git clone ssh://git@localhost:2222/admin/sentiment-base.git
```

The SSH username is ignored — you are identified by your key — but `git@` is the
conventional thing to write. Paths accept `{namespace}/{name}`, `models/{namespace}/{name}`
or `datasets/{namespace}/{name}`, with or without the `.git` suffix.

### What SSH does not offer

The SSH server exists to run two commands, `git-upload-pack` and `git-receive-pack`, and
refuses everything else: no shell, no PTY, no sftp or scp, no port forwarding. Connecting
without a command gets you a greeting saying so.

!!! warning "Git LFS needs the HTTP remote"

    Git LFS transfers over HTTP even when the git remote is SSH, and it normally discovers
    the endpoint by running `git-lfs-authenticate` on the SSH host — a command this server
    does not offer. For repositories whose large files are LFS objects, clone over HTTP, or
    point git-lfs at the HTTP endpoint yourself with
    `git config lfs.url http://localhost:8080/datasets/admin/imdb-reviews/info/lfs` (which then
    needs HTTP credentials as well as your key).

Permissions are identical on both transports: the SSH path delegates repository lookup and
the write check to exactly the same code the HTTP path uses.

!!! note "Host keys on ephemeral storage"

    The server generates an SSH host key on first start at `TF_SSH_HOST_KEY_PATH`. If that
    path is not persistent, every restart mints a new identity and every client warns about a
    host key mismatch. See [Configuration](../self-hosting/configuration.md).

## Branches, tags and revisions

New repositories are created with `main` as the default branch and one initial commit
containing `README.md` and `.gitattributes`.

Branches and tags work normally:

```bash
git checkout -b experiment
git push origin experiment

git tag v1.0
git push origin v1.0
```

`huggingface_hub` can do the same without a clone, and those calls work against thinkingface
too:

```python
from huggingface_hub import HfApi

api = HfApi()
api.create_branch("admin/my-model", branch="experiment")
api.create_tag("admin/my-model", tag="v1.0", tag_message="first release")
api.delete_branch("admin/my-model", branch="experiment")
api.delete_tag("admin/my-model", tag="v1.0")
```

`create_branch(..., revision=...)` starts the branch from any revision, and
`create_tag(..., revision=...)` tags one. `exist_ok=True` makes a repeat call a no-op. The
repository's **default branch cannot be deleted** — HEAD, the repository card and every
revision-less read depend on it. Creating a branch or a tag needs the same write access a push
does, and is refused on an archived repository.

Anywhere the API or the UI asks for a revision — `hf_hub_download(revision=...)`, a `resolve`
URL, the file browser's revision selector — you can name a branch, a tag or a commit SHA.
Annotated tags are resolved to the commit they point at.

Two behaviours to keep in mind:

- **Non-default branches are indexed too.** Pushing a branch publishes its files to object
  storage and refreshes the file index for that branch. What only the default branch updates
  is the repository's own metadata: the card parsed from `README.md`, the license and tags,
  the size shown in listings, and the lineage graph.
- **Creating only a tag schedules no indexing.** The indexing worker is triggered by branch
  tips moving, whether they move by `git push` or through `create_branch()`. A tag pointing at
  a commit that is already on a branch is fine — the files are indexed already — but a revision
  that exists nowhere else may not appear in the file index that the bucket-access script is
  generated from.

## What a push triggers on the server

When a branch tip moves, a background worker publishes the revision's non-LFS files to object
storage, rebuilds the file index, reads the footer of each Parquet file for schema and row
counts, parses the README card, and refreshes the experiment run index where relevant. It
normally finishes within a second or so of the push.

That is why a Parquet file becomes browsable in the [dataset viewer](dataset-viewer.md)
shortly after you push it, without you doing anything. [Uploading
Files](uploading.md#what-happens-after-an-upload) describes each step.

## Known limitations

- **Smart HTTP only.** The dumb HTTP protocol is not served; the server says so plainly
  rather than letting an old client fail obscurely. Any current git is fine.
- **Archived repositories reject pushes** over both transports, with a message telling you to
  unarchive the repository first.
- **Git LFS over an SSH remote** needs the HTTP endpoint configured, as described above.
- **Repositories have no visibility setting.** Anyone who can reach the server can clone any
  repository; permissions gate writing only. See [Downloading
  Files](downloading.md#who-can-read-what).

## Next steps

- [Uploading Files](uploading.md) — the non-git routes, and how LFS routing is decided.
- [Downloading Files](downloading.md) — `resolve` URLs and reading the bucket directly.
- [Authentication](../reference/authentication.md) — tokens, scopes and SSH keys.
