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
	"gopkg.in/yaml.v3"
)

type category struct {
	Name       string        `yaml:"name"`
	Subcat     []category    `yaml:"subcat"` // subcat always has this field as nil
	RemindTime time.Duration `yaml:"time"`
}

type buttonData struct {
	Cat     string
	Subcat  string
	Subcats []string // TODO: remove this (can overflow button data limit)
}

func createKeyboard(cats []category) tgbotapi.InlineKeyboardMarkup {
	curRow := make([]tgbotapi.InlineKeyboardButton, 0, 3)
	var keyboard [][]tgbotapi.InlineKeyboardButton

	for i := range cats {
		if cap(curRow) == len(curRow) {
			keyboard = append(keyboard, curRow)
			curRow = make([]tgbotapi.InlineKeyboardButton, 0, 3)
		}
		subcatNames := make([]string, len(cats[i].Subcat))
		for _, cat := range cats[i].Subcat {
			subcatNames = append(subcatNames, cat.Name)
		}
		subCutJson, _ := json.Marshal(&buttonData{
			Cat:     cats[i].Name,
			Subcats: subcatNames,
		})
		curRow = append(curRow, tgbotapi.NewInlineKeyboardButtonData(cats[i].Name, string(subCutJson)))
	}
	keyboard = append(keyboard, curRow)
	return tgbotapi.InlineKeyboardMarkup{
		InlineKeyboard: keyboard,
	}
}

func createKeyboardFroSubcat(chosedCat string, cats []string) tgbotapi.InlineKeyboardMarkup {
	var keyboard []tgbotapi.InlineKeyboardButton
	for i := range cats {
		subCutJson, _ := json.Marshal(&buttonData{
			Cat:    chosedCat,
			Subcat: cats[i],
		})
		keyboard = append(keyboard, tgbotapi.NewInlineKeyboardButtonData(cats[i], string(subCutJson)))
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

func sendByCallback(bot *tgbotapi.BotAPI, update *tgbotapi.Update, msg string, replyMarkup any) {
	response := tgbotapi.NewMessage(update.FromChat().ID, msg)
	if replyMarkup != nil {
		response.ReplyMarkup = replyMarkup
	}
	if _, err := bot.Send(response); err != nil {
		slog.Error("cannot send", "msg", err)
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
	closeKeyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("stop", "stop")))

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	var chatId int64 // since we use bot only for 1 chat, we can just save it here // todo: save it, so restart don't break timer
	var timer *time.Timer

Loop:
	for {
		select {
		case <-sigc:
			break Loop
		case update := <-updates:
			if len(idsWL) != 0 && !slices.Contains(idsWL, update.FromChat().ID) {
				slog.Warn("message from non white list chat", "id", update.FromChat().ID)
				continue
			}
			chatId = update.FromChat().ID
			if update.Message != nil {
				slog.Info("Message", "username", update.Message.From.UserName, "id", update.FromChat().ID, "text", update.Message.Text)
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, update.Message.Text)

				switch update.Message.Text {
				case "/open":
					msg.ReplyMarkup = mainKeyboard
				case "/stop":
					tCmd := exec.Command("timew", "stop")
					if err != nil {
						msg.Text = fmt.Sprintf("error: %s", err.Error())
					} else {
						output, err := tCmd.Output()
						if err != nil {
							slog.Error("cannot run timew", "msg", err)
						}
						msg.Text = string(output)
					}
				case "/status":
					// TODO
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
						sendByCallback(bot, &update, fmt.Sprintf("error: %s", err.Error()), nil)
						continue
					}
					if timer != nil {
						timer.Stop()
					}
					output, err := tCmd.Output()
					if err != nil {
						slog.Error("cannot send", "msg", err)
					}
					sendByCallback(bot, &update, string(output), nil)
					continue
				}

				var data buttonData
				err := json.Unmarshal([]byte(update.CallbackQuery.Data), &data)
				if err != nil {
					sendByCallback(bot, &update, fmt.Sprintf("cannot unmarshal button data: %s", err.Error()), nil)
					continue
				}
				if len(data.Subcats) != 0 {
					sendByCallback(bot, &update, "choose subcat", createKeyboardFroSubcat(data.Cat, data.Subcats))
				} else {
					if data.Cat == "" {
						slog.Error("cat is empty")
					}
					var tCmd *exec.Cmd
					if data.Subcat != "" {
						tCmd = exec.Command("timew", "start", data.Cat, data.Subcat)
					} else {
						tCmd = exec.Command("timew", "start", data.Cat)
					}
					if err != nil {
						sendByCallback(bot, &update, fmt.Sprintf("error: %s", err.Error()), nil)
						continue
					}
					dt := getTimerDuration(cats, data.Cat, data.Subcat)
					if timer != nil {
						timer.Stop()
					}

					slog.Info("Start command", "cmd", tCmd, "timer", dt)
					output, err := tCmd.Output()
					if err != nil {
						sendByCallback(bot, &update, fmt.Sprintf("error: %s", err.Error()), nil)
						continue
					}
					sendByCallback(bot, &update, string(output), closeKeyboard)

					timer = time.AfterFunc(dt, func() {
						msg := tgbotapi.NewMessage(chatId, "are you still doing it?")
						if _, err := bot.Send(msg); err != nil {
							slog.Error("cannot send", "msg", err)
						}
						timer.Reset(dt)
					})
				}
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
