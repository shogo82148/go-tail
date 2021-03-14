[![Test](https://github.com/shogo82148/go-tail/actions/workflows/go.yml/badge.svg)](https://github.com/shogo82148/go-tail/actions/workflows/go.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/shogo82148/go-tail.svg)](https://pkg.go.dev/github.com/shogo82148/go-tail)

# go-tail
go-tail is an implementation of tail -F

``` go
tail, err = tail.NewTailFile("something.log")
for {
  select {
  case line := <- tail.Lines:
    log.Print(line.Text)
  case err := <- tail.Errors:
    log.Print(err)
  }
}
```

# SEE ALSO

- https://github.com/mattn/gotail
- https://github.com/ActiveState/tail
- https://github.com/fujiwara/fluent-agent-hydra/blob/master/hydra/in_tail.go
