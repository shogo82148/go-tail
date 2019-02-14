[![Build Status](https://travis-ci.org/shogo82148/go-tail.svg?branch=master)](https://travis-ci.org/shogo82148/go-tail)
[![GoDoc](https://godoc.org/github.com/shogo82148/go-tail?status.svg)](https://godoc.org/github.com/shogo82148/go-tail)

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
