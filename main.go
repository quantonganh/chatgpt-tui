package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mitchellh/go-homedir"
	"github.com/rivo/tview"
	"github.com/tidwall/buntdb"
)

const (
	roleUser      = "user"
	roleAssistant = "assistant"

	prefixSuggestTitle = "suggest me a short title for "
	suffixTime         = ":time"
)

type history struct {
	title        string
	conversation string
	time         time.Time
}

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Println("Please set `OPENAI_API_KEY` environment variable. You can find your API key at https://platform.openai.com/account/api-keys.")
		return
	}

	app := tview.NewApplication()

	home, err := homedir.Dir()
	if err != nil {
		log.Panic(err)
	}

	dbPath := filepath.Join(home, ".chatgpt")
	if err := os.MkdirAll(dbPath, 0700); err != nil {
		log.Panic(err)
	}

	db, err := buntdb.Open(filepath.Join(dbPath, "history.db"))
	if err != nil {
		log.Panic(err)
	}
	defer db.Close()

	textView := tview.NewTextView().
		SetChangedFunc(func() {
			app.Draw()
		}).
		SetDynamicColors(true).
		SetRegions(true).
		SetWordWrap(true)
	textView.SetTitle("Conversation").SetBorder(true)

	textArea := tview.NewTextArea()
	textArea.SetTitle("Question").SetBorder(true)

	list := tview.NewList()
	list.SetTitle("History").SetBorder(true)
	messages := make([]Message, 0)
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 'j':
			if list.GetCurrentItem() < list.GetItemCount() {
				list.SetCurrentItem(list.GetCurrentItem() + 1)
			}
		case 'k':
			if list.GetCurrentItem() > 0 {
				list.SetCurrentItem(list.GetCurrentItem() - 1)
			}
		case 'd':
			currentIndex := list.GetCurrentItem()
			currentTitle, _ := list.GetItemText(currentIndex)
			list.RemoveItem(currentIndex)

			if list.GetItemCount() == 0 {
				textView.Clear()
			}

			db.Update(func(tx *buntdb.Tx) error {
				var title string
				tx.AscendKeys(currentTitle, func(k, v string) bool {
					if k == currentTitle {
						title = k
					}
					return true
				})

				if _, err := tx.Delete(title); err != nil {
					return err
				}

				if _, err := tx.Delete(fmt.Sprintf("%s%s", title, suffixTime)); err != nil {
					return err
				}

				return nil
			})
		}

		return event
	})
	histories := make([]history, 0)
	db.View(func(tx *buntdb.Tx) error {
		err := tx.Ascend("", func(key, value string) bool {
			h := history{}
			if !strings.HasSuffix(key, suffixTime) {
				h.title = key
				h.conversation = value

				err = db.View(func(tx *buntdb.Tx) error {
					val, err := tx.Get(key + suffixTime)
					if err != nil {
						return err
					}
					t, err := time.Parse(time.RFC3339, val)
					if err != nil {
						return err
					}
					h.time = t
					return nil
				})
				histories = append(histories, h)
			}
			return true
		})
		sort.Slice(histories, func(i, j int) bool {
			return histories[i].time.After(histories[j].time)
		})
		return err
	})
	log.Printf("histories: %d", len(histories))
	for i := range histories {
		list.AddItem(histories[i].title, "", rune(0), func() {
			textView.SetText(histories[i].conversation)
		})
	}
	list.SetChangedFunc(func(index int, title string, secondaryText string, shortcut rune) {
		db.View(func(tx *buntdb.Tx) error {
			value, err := tx.Get(title)
			if err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(value), &messages); err != nil {
				return err
			}
			textView.SetText(toConversation(messages))
			return nil
		})
	})
	list.SetSelectedFunc(func(index int, title string, secondaryText string, shortcut rune) {
		db.View(func(tx *buntdb.Tx) error {
			value, err := tx.Get(title)
			if err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(value), &messages); err != nil {
				return err
			}
			textView.SetText(toConversation(messages))
			return nil
		})

		app.SetFocus(textArea)
	})

	if list.GetItemCount() > 0 {
		title, _ := list.GetItemText(list.GetCurrentItem())
		db.View(func(tx *buntdb.Tx) error {
			value, err := tx.Get(title)
			if err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(value), &messages); err != nil {
				return err
			}
			textView.SetText(toConversation(messages))
			return nil
		})
	}

	textArea.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			content := textArea.GetText()
			if strings.TrimSpace(content) == "" {
				return nil
			}
			textArea.SetText("", false)
			textArea.SetDisabled(true)

			textView.ScrollToEnd()
			if textView.GetText(false) != "" {
				fmt.Fprintf(textView, "\n\n")
			}
			fmt.Fprintln(textView, "[red::]You:[-]")
			fmt.Fprintf(textView, "%s\n\n", content)

			messages = append(messages, Message{
				Role:    roleUser,
				Content: content,
			})

			titleCh := make(chan string)
			go func() {
				resp, err := createChatCompletion([]Message{
					{
						Role:    roleUser,
						Content: prefixSuggestTitle + content,
					},
				}, false)
				if err != nil {
					log.Panic(err)
				}
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				if err != nil {
					log.Panic(err)
				}

				var titleResp *Response
				if err := json.Unmarshal(body, &titleResp); err == nil {
					titleCh <- titleResp.Choices[0].Message.Content
				}
			}()

			respCh := make(chan string)
			go func() {
				resp, err := createChatCompletion(messages, true)
				if err != nil {
					log.Panic(err)
				}

				reader := bufio.NewReader(resp.Body)
				for {
					line, err := reader.ReadBytes('\n')
					if err == nil {
						var streamingResp *StreamingResponse
						if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &streamingResp); err == nil {
							respCh <- streamingResp.Choices[0].Delta.Content
						}
					} else if errors.Is(err, io.EOF) {
						close(respCh)
						return
					}
				}
			}()

			fmt.Fprintln(textView, "[green::]ChatGPT:[-]")
			go func() {
				var fullContent strings.Builder
				for deltaContent := range respCh {
					fmt.Fprintf(textView, deltaContent)
					fullContent.WriteString(deltaContent)
				}

				messages = append(messages, Message{
					Role:    roleAssistant,
					Content: fullContent.String(),
				})

				if len(messages) == 2 {
					list.InsertItem(0, strings.Trim(<-titleCh, "\""), "", rune(0), nil)
					list.SetCurrentItem(0)
				}

				title, _ := list.GetItemText(list.GetCurrentItem())
				value, err := json.Marshal(messages)
				if err != nil {
					log.Panic(err)
				}
				db.Update(func(tx *buntdb.Tx) error {
					_, _, err := tx.Set(title, string(value), nil)
					if err != nil {
						return err
					}

					_, _, err = tx.Set(fmt.Sprintf("%s%s", title, suffixTime), time.Now().Format(time.RFC3339), nil)
					return err
				})

				fmt.Fprintf(textView, "\n\n")
				textArea.SetDisabled(false)
			}()

			return nil
		}
		return event
	})

	button := tview.NewButton("+ New chat").SetSelectedFunc(func() {
		messages = nil
		textView.Clear()
		app.SetFocus(textArea)
	})
	button.SetBorder(true)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyF1:
			app.SetFocus(button)
		case tcell.KeyF2:
			app.SetFocus(list)
		case tcell.KeyF3:
			app.SetFocus(textView)
		case tcell.KeyF4:
			app.SetFocus(textArea)
		case tcell.KeyESC:
			app.Stop()
		default:
			return event
		}
		return nil
	})

	help := tview.NewTextView().SetRegions(true).SetDynamicColors(true)
	help.SetText("F1: new chat, F2: history, F3: conversation, F4: question, enter: submit, j/k: navigate, d: delete, esc: quit").SetTextAlign(tview.AlignCenter)

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tview.NewFlex().SetDirection(tview.FlexColumn).
			AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
				AddItem(button, 3, 1, false).
				AddItem(list, 0, 1, false), 0, 1, false).
			AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
				AddItem(textView, 0, 1, false).
				AddItem(textArea, 5, 1, false), 0, 3, false), 0, 1, false).
		AddItem(help, 1, 1, false)
	if err := app.SetRoot(flex, true).SetFocus(textArea).Run(); err != nil {
		panic(err)
	}
}

const completionsURL = "https://api.openai.com/v1/chat/completions"

func createChatCompletion(messages []Message, stream bool) (*http.Response, error) {
	reqBody, err := json.Marshal(&Request{
		Model:    "gpt-3.5-turbo",
		Messages: messages,
		Stream:   stream,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, completionsURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
	req.Header.Add("Content-Type", "application/json")

	client := &http.Client{}
	return client.Do(req)
}

type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int    `json:"created"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type StreamingResponse struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int    `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Index        int         `json:"index"`
		FinishReason interface{} `json:"finish_reason"`
	} `json:"choices"`
}

func toConversation(messages []Message) string {
	contents := make([]string, 0)
	for _, msg := range messages {
		switch msg.Role {
		case roleUser:
			msg.Content = fmt.Sprintf("[red::]You:[-]\n%s", msg.Content)
		case roleAssistant:
			msg.Content = fmt.Sprintf("[green::]ChatGPT:[-]\n%s", msg.Content)
		}
		contents = append(contents, msg.Content)
	}
	return strings.Join(contents, "\n\n")
}
