package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/scanner"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/exp/maps"
	"gopkg.in/yaml.v3"
)

type category struct {
	Name       string        `yaml:"name"`
	Subcat     []category    `yaml:"subcat"` // subcat always has this field as nil
	RemindTime time.Duration `yaml:"time"`
}

type buttonData struct {
	Cat    string
	Subcat string
}

var closeKeyboard = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("stop", "stop")))

func createKeyboard(cats []category) tgbotapi.ReplyKeyboardMarkup {
	curRow := make([]tgbotapi.KeyboardButton, 0, 4)
	var keyboard [][]tgbotapi.KeyboardButton

	for i := range cats {
		if cap(curRow) == len(curRow) {
			keyboard = append(keyboard, curRow)
			curRow = make([]tgbotapi.KeyboardButton, 0, 4)
		}
		subcatNames := make([]string, len(cats[i].Subcat))
		for _, cat := range cats[i].Subcat {
			subcatNames = append(subcatNames, cat.Name)
		}
		curRow = append(curRow, tgbotapi.NewKeyboardButton(cats[i].Name))
	}
	keyboard = append(keyboard, curRow)
	return tgbotapi.ReplyKeyboardMarkup{
		Keyboard: keyboard,
	}
}

func createKeyboardFroSubcat(chosedCat string, cats []category) tgbotapi.InlineKeyboardMarkup {
	var keyboard []tgbotapi.InlineKeyboardButton
	for i := range cats {
		subCutJson, _ := json.Marshal(&buttonData{
			Cat:    chosedCat,
			Subcat: cats[i].Name,
		})
		keyboard = append(keyboard, tgbotapi.NewInlineKeyboardButtonData(cats[i].Name, string(subCutJson)))
	}
	return tgbotapi.NewInlineKeyboardMarkup(keyboard)
}

func deleteKeyboard(bot *tgbotapi.BotAPI, update *tgbotapi.Update) {
	edit := tgbotapi.NewEditMessageReplyMarkup(
		update.FromChat().ID,
		update.CallbackQuery.Message.MessageID,
		tgbotapi.InlineKeyboardMarkup{InlineKeyboard: make([][]tgbotapi.InlineKeyboardButton, 0, 0)},
	)
	_, err := bot.Send(edit)
	if err != nil {
		slog.Error("edit responce", "err", err)
	}
}

func checkActiveTracker() bool {
	cmd := exec.Command("timew")
	output, err := cmd.Output()
	if err != nil {
		slog.Error("cannot call timew", "msg", err)
	}
	return strings.HasPrefix(string(output), "Tracking")
}

func getTimerDuration(cats []category, catName string, subcatName string) time.Duration {
	for i := range cats {
		if cats[i].Name == catName {
			if subcatName == "" {
				return cats[i].RemindTime
			}
			for j := range cats[i].Subcat {
				if cats[i].Subcat[j].Name == subcatName {
					return cats[i].Subcat[j].RemindTime
				}
			}
		}
	}
	return 0
}

type Warden struct {
	bot    *tgbotapi.BotAPI
	timer  *time.Timer
	cats   []category
	chatID int64
}

func (w *Warden) runTimew(cat string, subcat string) {

	var tCmd *exec.Cmd
	if subcat != "" {
		tCmd = exec.Command("timew", "start", cat, subcat)
	} else {
		tCmd = exec.Command("timew", "start", cat)
	}
	dt := getTimerDuration(w.cats, cat, subcat)
	if w.timer != nil {
		w.timer.Stop()
	}

	slog.Info("Start command", "cmd", tCmd, "timer", dt)
	output, err := tCmd.Output()
	if err != nil {
		w.send(fmt.Sprintf("error: %s", err.Error()), nil)
		return
	}
	w.send(string(output), closeKeyboard)

	w.timer = time.AfterFunc(dt, func() {
		msg := tgbotapi.NewMessage(w.chatID, "are you still doing it?")
		if _, err := w.bot.Send(msg); err != nil {
			slog.Error("cannot send", "msg", err)
		}
		w.timer.Reset(dt) // TODO: fix race
	})
}

func (w *Warden) send(msg string, replyMarkup any) {
	response := tgbotapi.NewMessage(w.chatID, msg)
	if replyMarkup != nil {
		response.ReplyMarkup = replyMarkup
	}
	if _, err := w.bot.Send(response); err != nil {
		slog.Error("cannot send", "msg", err)
	}
}

func (w *Warden) CategoryChoosen(cat string) {
	for i := range w.cats {
		if w.cats[i].Name == cat {
			if len(w.cats[i].Subcat) == 0 {
				w.runTimew(cat, "")
			} else {
				msg := tgbotapi.NewMessage(w.chatID, "choose subcat")
				msg.ReplyMarkup = createKeyboardFroSubcat(cat, w.cats[i].Subcat)
				if _, err := w.bot.Send(msg); err != nil {
					slog.Error("cannot send", "msg", err)
				}
			}
			return
		}
	}
	msg := tgbotapi.NewMessage(w.chatID, "unknown category")
	if _, err := w.bot.Send(msg); err != nil {
		slog.Error("cannot send", "msg", err)
	}
}

func main() {
	runtime.GOMAXPROCS(1)
	initLog()
	tokenFile := flag.String("token-file", "token", "telegram api token")
	categoryFile := flag.String("categories", "category.yml", "file with list of categories")
	flag.Parse()
	tokenRaw, err := os.ReadFile(*tokenFile)
	if err != nil {
		slog.Error("cannot read token file", "error", err)
		os.Exit(1)
	}
	token := strings.TrimSpace(string(tokenRaw))
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		slog.Error("cannot connect to telegram api", "error", err)
		os.Exit(1)
	}

	configRaw, err := os.ReadFile(*categoryFile)
	if err != nil {
		slog.Error("cannot open category file", "error", err)
		os.Exit(1)
	}
	var cats []category
	err = yaml.Unmarshal(configRaw, &cats)
	if err != nil {
		slog.Error("cannot parse category file", "error", err)
		os.Exit(1)
	}

	idsWL := loadWhiteList()
	// bot.Debug = true

	slog.Info("here we go")

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	mainKeyboard := createKeyboard(cats)

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	weekReportTimer := time.NewTimer(getDurationToReport())

	w := Warden{
		bot:  bot,
		cats: cats,
	}
Loop:
	for {
		select {
		case <-sigc:
			break Loop
		case <-weekReportTimer.C:
			report := generateWeeklyReport()
			msg := tgbotapi.NewMessage(w.chatID, report)
			if _, err := bot.Send(msg); err != nil {
				slog.Error("cannot send", "msg", err)
			}
			weekReportTimer = time.NewTimer(getDurationToReport())

		case update := <-updates:
			if len(idsWL) != 0 && !slices.Contains(idsWL, update.FromChat().ID) {
				slog.Warn("message from non white list chat", "id", update.FromChat().ID)
				continue
			}
			w.chatID = update.FromChat().ID // TODO: just get from whitelist?
			if update.Message != nil {
				slog.Info("Message", "username", update.Message.From.UserName, "id", update.FromChat().ID, "text", update.Message.Text)
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, update.Message.Text)

				switch update.Message.Text {
				case "/open":
					msg.ReplyMarkup = mainKeyboard
				case "/start":
					msg.ReplyMarkup = mainKeyboard
				case "/stop":
					output, err := exec.Command("timew", "stop").Output()
					if err != nil {
						slog.Error("cannot run timew", "msg", err)
					}
					msg.Text = string(output)
				case "/report":
					report := generateWeeklyReport()
					msg.Text = report

				default:
					w.CategoryChoosen(update.Message.Text)
					continue
				}
				if _, err := bot.Send(msg); err != nil {
					slog.Error("cannot send", "msg", err)
				}
			} else if update.CallbackQuery != nil { // keyboard callback
				slog.Info("Update with callbackQuery", "query_data", update.CallbackQuery.Data)
				callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "")
				if _, err := bot.Request(callback); err != nil {
					panic(err)
				}
				deleteKeyboard(bot, &update)

				if update.CallbackQuery.Data == "stop" {
					tCmd := exec.Command("timew", "stop")
					if err != nil {
						w.send(fmt.Sprintf("error: %s", err.Error()), nil)
						continue
					}
					if w.timer != nil {
						w.timer.Stop()
					}
					output, err := tCmd.Output()
					if err != nil {
						slog.Error("cannot send", "msg", err)
					}
					w.send(string(output), nil)
					continue
				}

				// User choosed cubcategory
				var data buttonData
				err := json.Unmarshal([]byte(update.CallbackQuery.Data), &data)
				if err != nil {
					w.send(fmt.Sprintf("cannot unmarshal button data: %s", err.Error()), nil)
					continue
				}
				if data.Cat == "" {
					slog.Error("cat is empty")
					w.send(fmt.Sprintf("internal error: category data is empty"), nil)
					continue
				}
				if data.Subcat == "" {
					slog.Error("subcat is empty")
					w.send(fmt.Sprintf("internal error: subcategory data is empty"), nil)
					continue
				}
				w.runTimew(data.Cat, data.Subcat)
			}
		}
	}
}

func initLog() {
	th := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr { // omfg, slog has trully opaque interface (as most things in go)
			if a.Key == slog.SourceKey {
				s := a.Value.Any().(*slog.Source)
				s.File = path.Base(s.File)
			}
			return a
		},
	})
	slog.SetDefault(slog.New(th))
}

func loadWhiteList() []int64 {
	var idsWL []int64
	idsWLRaw, err := os.ReadFile("/etc/time_warden_wl")
	if err == nil {
		var s scanner.Scanner
		s.Init(strings.NewReader(string(idsWLRaw)))
		for tok := s.Scan(); tok != scanner.EOF; tok = s.Scan() {
			if id, err := strconv.Atoi(s.TokenText()); err == nil {
				idsWL = append(idsWL, int64(id))
			}
		}
	}
	slog.Info("white list", "ids", idsWL)
	return idsWL
}

func getDurationToReport() time.Duration {
	now := time.Now()
	referenceTime := now.Add(time.Hour + time.Minute * 31) // so we hit next week at time of report
	year, week := referenceTime.ISOWeek()
	weekStart := weekStart(year, week)
	weekEnd := weekStart.AddDate(0, 0, 6).Add(time.Hour * 22 + time.Minute * 30) // to sunday 22:30
	return weekEnd.Sub(now)
}

func weekStart(year, week int) time.Time {
	t := time.Date(year, 7, 1, 0, 0, 0, 0, time.Local)

	// Roll back to Monday:
	if wd := t.Weekday(); wd == time.Sunday {
		t = t.AddDate(0, 0, -6)
	} else {
		t = t.AddDate(0, 0, -int(wd)+1)
	}

	_, w := t.ISOWeek()
	t = t.AddDate(0, 0, (week-w)*7)
	return t
}


func generateWeeklyReport() string {
	output, err := exec.Command("timew", "export", ":week").Output()
	if err != nil {
		slog.Error("cannot run timew", "msg", err)
		return "cannot generate summary: " + err.Error()
	}
	type event struct {
		Start string
		End   string
		Tags  []string
	}
	var events []event
	err = json.Unmarshal(output, &events)
	if err != nil {
		return "cannot unmarshall: " + err.Error()
	}

	reportDur := make(map[string]time.Duration)
	for _, ev := range events {
		if ev.Start == "" || ev.End == "" {
			continue
		}
		const layout = "20060102T150405Z"
		start, err := time.Parse(layout, ev.Start)
		if err != nil {
			return "cannot parse time: " + err.Error()
		}
		end, err := time.Parse(layout, ev.End)
		if err != nil {
			return "cannot parse time: " + err.Error()
		}
		dur := end.Sub(start)
		for _, tag := range ev.Tags {
			acc := reportDur[tag]
			reportDur[tag] = dur + acc
		}
	}

	keys := maps.Keys(reportDur)
	slices.Sort(keys)
	report := []string{"Week report"}
	for _, key := range keys {
		report = append(report, fmt.Sprintf("%s: %s", key, reportDur[key].Round(time.Minute)))
	}

	return strings.Join(report, "\n")
}
