# Security policy

## Reporting a vulnerability

Report it privately, with GitHub's
[**Report a vulnerability**](https://github.com/JohanLindvall/arena/security/advisories/new)
form on the repository's Security tab. Please do not open a public issue or pull
request: the report and the fix stay private until a release carries the fix.

A useful report names the version (`go list -m github.com/JohanLindvall/arena`)
and includes the smallest program or test that shows the problem, ideally one
that fails under `go test -race`. If you are not sure whether something counts,
report it privately anyway.

Once the fix is released, a GitHub security advisory describes the problem and
names the first fixed version.

## Supported versions

Only the latest release is supported. The module is pre-1.0 and has no
maintenance branches: a fix lands on `main` and ships as the next release, so
the remedy for any affected version is to upgrade.

```sh
go get github.com/JohanLindvall/arena@latest
```

## What counts

The package hands out views over its own chunks through `unsafe`, so its
soundness is a security property. In a program that follows
[the rules](README.md#the-rules), these are vulnerabilities:

- a view or `Ref` that reaches outside the value it was handed out for — into
  another live value, or into memory the arena does not own;
- a string from `Intern` or `Str` whose bytes change before the next `Reset` or
  `Release`;
- memory corruption or a data race that originates in the package itself.

These are documented behaviour, not vulnerabilities:

- whatever follows from breaking the rules — concurrent use, a view or `Ref`
  used after `Reset` or `Release` or against another arena, writing to a view
  from `Append` or `Value`, copying an arena after first use. Nothing checks
  them, by design;
- a region from `Reserve` that holds a previous batch's data. `Reset` does not
  clear chunks, and `Reserve` does not clear what it hands back;
- `AppendRef` and `StrRef` panicking on a value stored beyond what a `Ref` can
  describe; see [what a `Ref` can address](README.md#what-a-ref-can-address).
