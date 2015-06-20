# go-tail
go-tail is an implementation of tail -F

``` go
tail, err = tail.New("something.log")
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
