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
