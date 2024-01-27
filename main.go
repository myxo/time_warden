package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gopkg.in/yaml.v3"
)

type category struct {
	Name   string
	Subcat []string
}

type categories struct {
	cats []category
}

// manual unmarshall to preserve list order
func (c *categories) UnmarshalYAML(value *yaml.Node) error {
	var lastCat *category
	for _, content := range value.Content {
		switch content.Kind {
		case yaml.SequenceNode:
			if content.Tag == "!!seq" {
				for i := range content.Content {
					lastCat.Subcat = append(lastCat.Subcat, content.Content[i].Value)
				}
			}
		case yaml.ScalarNode:
			if content.Tag == "!!str" {
				c.cats = append(c.cats, category{Name: content.Value})
				lastCat = &c.cats[len(c.cats)-1]
			}
		default:
		}
	}
	return nil
}

type buttonData struct {
	Cat     string
	Subcat  string
	Subcats []string
}

func createKeyboard(cats categories) tgbotapi.InlineKeyboardMarkup {
	curRow := make([]tgbotapi.InlineKeyboardButton, 0, 3)
	var keyboard [][]tgbotapi.InlineKeyboardButton

	for i := range cats.cats {
		if cap(curRow) == len(curRow) {
			keyboard = append(keyboard, curRow)
			curRow = make([]tgbotapi.InlineKeyboardButton, 0, 3)
		}
		subCutJson, _ := json.Marshal(&buttonData{
			Cat:     cats.cats[i].Name,
			Subcats: cats.cats[i].Subcat,
		})
		curRow = append(curRow, tgbotapi.NewInlineKeyboardButtonData(cats.cats[i].Name, string(subCutJson)))
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

func main() {
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
	var cat categories
	err = yaml.Unmarshal(configRaw, &cat)
	if err != nil {
		slog.Error("cannot parse category file", "error", err)
		os.Exit(1)
	}

	// bot.Debug = true

	slog.Info("here we go")

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	mainKeyboard := createKeyboard(cat)
	closeKeyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("close", "close")))

	for update := range updates {
		if update.Message != nil {
			slog.Info("Message", "username", update.Message.From.UserName, "text", update.Message.Text)
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
		} else if update.CallbackQuery != nil {
			slog.Info("Update with callbackQuery", "query_data", update.CallbackQuery.Data)
			callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "")
			if _, err := bot.Request(callback); err != nil {
				panic(err)
			}
			deleteKeyboard(bot, &update)

			if update.CallbackQuery.Data == "close" {
				tCmd := exec.Command("timew", "stop")
				if err != nil {
					sendByCallback(bot, &update, fmt.Sprintf("error: %s", err.Error()), nil)
					continue
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
				sendByCallback(bot, &update, fmt.Sprintf("error: %s", err.Error()), nil)
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

				slog.Info("Start command", "cmd", tCmd)
				output, err := tCmd.Output()
				if err != nil {
					sendByCallback(bot, &update, fmt.Sprintf("error: %s", err.Error()), nil)
					continue
				}
				sendByCallback(bot, &update, string(output), closeKeyboard)
				// TODO: set timer to check if we abandon task
			}
		}
	}
}
