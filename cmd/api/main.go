package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Jstarzz/web-scraper/internal/api"
	"github.com/Jstarzz/web-scraper/internal/config"
	"github.com/Jstarzz/web-scraper/internal/store"
)

func main(){
	log:=slog.New(slog.NewJSONHandler(os.Stdout,nil))
	cfg,err:=config.Load();if err!=nil{log.Error("config", "error",err);os.Exit(1)}
	ctx:=context.Background();st,err:=store.Open(ctx,cfg.DatabaseURL);if err!=nil{log.Error("database", "error",err);os.Exit(1)};defer st.Close()
	if err:=st.Migrate(ctx);err!=nil{log.Error("migration","error",err);os.Exit(1)}
	httpServer:=&http.Server{Addr:cfg.APIAddr,Handler:api.New(st,cfg.AdminToken,log),ReadHeaderTimeout:5*time.Second,IdleTimeout:60*time.Second}
	go func(){log.Info("api listening","addr",cfg.APIAddr);if err:=httpServer.ListenAndServe();err!=nil&&err!=http.ErrServerClosed{log.Error("http server","error",err);os.Exit(1)}}()
	stop:=make(chan os.Signal,1);signal.Notify(stop,syscall.SIGINT,syscall.SIGTERM);<-stop
	shutdown,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel();_ = httpServer.Shutdown(shutdown)
}
