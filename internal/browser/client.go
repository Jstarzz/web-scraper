package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Jstarzz/web-scraper/internal/model"
)

type Client struct { baseURL string; http *http.Client }

type response struct { Listings []model.Listing `json:"listings"`; Error string `json:"error,omitempty"` }

func New(baseURL string, timeout time.Duration)*Client{return &Client{baseURL:strings.TrimRight(baseURL,"/"),http:&http.Client{Timeout:timeout}}}

func (c *Client) Scrape(ctx context.Context,job model.Job)([]model.Listing,error){
	body,_:=json.Marshal(map[string]any{"marketplace":job.Marketplace,"query":job.Query,"limit":job.Limit})
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,c.baseURL+"/scrape",bytes.NewReader(body));if err!=nil{return nil,err};req.Header.Set("Content-Type","application/json")
	res,err:=c.http.Do(req);if err!=nil{return nil,err};defer res.Body.Close()
	var payload response;if err:=json.NewDecoder(http.MaxBytesReader(nil,res.Body,4<<20)).Decode(&payload);err!=nil{return nil,err}
	if res.StatusCode/100!=2{return nil,fmt.Errorf("browser worker status %d: %s",res.StatusCode,payload.Error)}
	if len(payload.Listings)==0{return nil,fmt.Errorf("browser worker returned no listings")}
	return payload.Listings,nil
}
