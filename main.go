package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"

	sessions "github.com/goincremental/negroni-sessions"
	"github.com/goincremental/negroni-sessions/cookiestore"
	"github.com/julienschmidt/httprouter"
	"github.com/unrolled/render"
	"github.com/urfave/negroni"
)

var renderer *render.Render
var store sessions.Store
var userdb UserDao

func init() {
	// Create new renderer
	renderer = render.New(render.Options{
		Directory: "web",
	})
	mdao := &MongoDao{
		URL: "mongodb+srv://" + os.Getenv("ATLAS_USER") + ":" +
			os.Getenv("ATLAS_PASS") + "@" + os.Getenv("ATLAS_URI"),
		DBName: databaseName,
	}
	if err := mdao.Connect(); err != nil {
		log.Fatal(err)
	}
	userdb = mdao

	updateCorpList()
	go dailyUpdateCorps()

	loadCorpMap()
}

const (
	sessionKey    = "MBWSserver-session-key"
	sessionSecret = "MBWSserver-session-secret"
)

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func main() {
	router := httprouter.New()
	router.GET("/", renderMainView)
	router.GET("/auth/:action/:provider", loginHandler)
	router.GET("/logout", logoutHandler)
	router.GET("/stocks/getCode", getStockCode)
	router.POST("/stocks/:action", transactHandler)

	n := negroni.Classic()

	store := cookiestore.New([]byte(sessionSecret))
	n.Use(sessions.Sessions(sessionKey, store))
	n.Use(sessionHandler("/auth"))

	n.UseHandler(router)
	n.Run(":9000")
}

type statusReturn struct {
	code   string
	status *StockStatus
}

func renderMainView(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	currentUser := getSessionUser(r)
	if currentUser != nil {
		// render User info
		user, err := userdb.FindUser(currentUser.Email)
		if err != nil {
			log.Println(err)
			renderer.HTML(w, http.StatusOK, "index", currentUser)
			return
		}

		statusChan := make(chan statusReturn)
		defer close(statusChan)
		doneChan := make(chan struct{})
		stockMap := make(StockStatusMap, 0)
		sum := StockStatus{}

		var wg sync.WaitGroup
		for code, stock := range user.Stocks {
			wg.Add(1)
			go stock.CalculateIncome(&wg, statusChan, code)
		}

		go func(wg *sync.WaitGroup, doneChan chan<- struct{}) {
			wg.Wait()
			close(doneChan)
		}(&wg, doneChan)

	WAITLOOP:
		for {
			select {
			case ret := <-statusChan:
				status := ret.status
				stockMap[ret.code] = status
				sum.Spent += status.Spent
				sum.Earned += status.Earned
				sum.Remain += status.Remain
				sum.Income += status.Income
			case <-doneChan:
				break WAITLOOP
			}
		}

		sum.Yield = float32((sum.Income)) / float32(sum.Spent) * 100

		renderer.HTML(w, http.StatusOK, "index", map[string]interface{}{
			"user":     user,
			"stockMap": stockMap,
			"sum":      sum,
		})
	} else {
		renderer.HTML(w, http.StatusOK, "index", nil)
	}
}

func loginHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	action := ps.ByName("action")
	provider := ps.ByName("provider")

	switch action {
	case "login":
		switch provider {
		case "google":
			loginByGoogle(w, r)
		default:
			http.Error(w,
				"Auth action '"+action+"' with provider '"+provider+"' is not supported.",
				http.StatusNotFound)
		}
	case "callback":
		switch provider {
		case "google":
			authByGoogle(w, r)
		default:
			http.Error(w,
				"Auth action '"+action+"' with provider '"+provider+"' is not supported.",
				http.StatusNotFound)
		}
	default:
		http.Error(w, "Auth action '"+action+"' is not supported.", http.StatusNotFound)
	}
}

func logoutHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	sessions.GetSession(r).Delete(currentUserKey)
	http.Redirect(w, r, "/", http.StatusFound)
}

func transactHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	sessionUser := getSessionUser(r)
	if sessionUser != nil {
		user, err := userdb.FindUser(sessionUser.Email)
		if err != nil {
			log.Println(err)
			goto RETURN
		}

		action := ps.ByName("action")
		err = r.ParseForm()
		if err != nil {
			log.Println(err)
			return
		}

		switch action {
		case "buy":
			stockName := r.Form.Get("bname")
			stockCode := r.Form.Get("bcode")
			quantity, _ := strconv.Atoi(r.Form.Get("bquantity"))
			price, _ := strconv.Atoi(r.Form.Get("bprice"))
			user.addTx(buy, stockCode, stockName, quantity, price)
		case "sell":
			stockName := r.Form.Get("sname")
			stockCode := r.Form.Get("scode")
			quantity, _ := strconv.Atoi(r.Form.Get("squantity"))
			price, _ := strconv.Atoi(r.Form.Get("sprice"))
			user.addTx(sell, stockCode, stockName, quantity, price)
		}
		userdb.UpdateUserStock(user)
	}
RETURN:
	http.Redirect(w, r, "/", http.StatusMovedPermanently)
}
