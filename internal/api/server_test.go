package api

import "testing"

func TestRateLimiter(t *testing.T){
	l:=newRateLimiter()
	if !l.allow("a",2){t.Fatal("first request should pass")}
	if !l.allow("a",2){t.Fatal("second request should pass")}
	if l.allow("a",2){t.Fatal("third request should be limited")}
	if !l.allow("b",1){t.Fatal("limits must be isolated per key")}
}
