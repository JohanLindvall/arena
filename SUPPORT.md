# Getting help

Start with the [quick start](README.md#quick-start),
[lifetime rules](README.md#the-rules), and
[runnable API examples](https://pkg.go.dev/github.com/JohanLindvall/arena#pkg-examples).

Search [existing issues](https://github.com/JohanLindvall/arena/issues)
before opening a new one. Questions are welcome even if you are new to Go or
arena allocation.

| What you need | Where to go |
| --- | --- |
| Help choosing an API or understanding behavior | [Ask a question](https://github.com/JohanLindvall/arena/issues/new?template=question.yml) |
| Unexpected behavior or a regression | [Report a bug](https://github.com/JohanLindvall/arena/issues/new?template=bug_report.yml) |
| A new feature or documentation improvement | [Suggest an improvement](https://github.com/JohanLindvall/arena/issues/new?template=feature_request.yml) |
| A suspected security vulnerability | [Report privately](SECURITY.md#reporting-a-vulnerability) |
| Help making a contribution | [Contributor guide](CONTRIBUTING.md) |

For a bug, include the smallest program or test that reproduces it, what you
expected, and what happened. These commands identify the relevant environment:

```sh
go version
go env GOOS GOARCH
go list -m github.com/JohanLindvall/arena
```

Run the last command in the application that imports arena. If you are testing
an arena checkout, include its commit from `git rev-parse HEAD` instead.
For a performance report, also include the workload, CPU, benchmark command,
and before/after results from the same machine.

Remove credentials and private application data from examples and logs.
Suspected memory corruption, data exposure, or security-sensitive races belong
in the private reporting channel, even if you are unsure of the cause.

Support happens in public issues so others can learn from the answer. This
project does not offer a guaranteed response time.
