package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v4"
)

type ModelInfo struct {
	Provider string
	Name     string
}

var (
	memory     = make(map[int64]string)
	userModels = make(map[int64]string)
	mu         sync.Mutex

	MODELS_INFO = map[string]ModelInfo{
		"gpt-3.5":  {Provider: "openrouter", Name: "openai/gpt-3.5-turbo"},
		"gpt-4":    {Provider: "openrouter", Name: "openai/gpt-4o"},
		"gemini":   {Provider: "gemini", Name: "gemini-2.0-flash"},
		"deepseek": {Provider: "openrouter", Name: "deepseek/deepseek-r1"},
		"qwen":     {Provider: "openrouter", Name: "qwen/qwen-plus"},
		"claude":   {Provider: "openrouter", Name: "anthropic/claude-3.5-haiku"},
		// "image/sora_v2": {Provider: "selenium", Name: "image/sora_v2"},
	}
)

const LOG_FILE = "bot.log"

func logEntry(userID int64, message, response, model string) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	entry := fmt.Sprintf("\n[%s] UserID: %d\n", ts, userID)
	if message != "" {
		entry += fmt.Sprintf("  Message: %s\n", message)
	}
	if response != "" {
		entry += fmt.Sprintf("  Response: %s\n", response)
	}
	if model != "" {
		entry += fmt.Sprintf("  Model: %s\n", model)
	}
	entry += strings.Repeat("-", 50)

	f, err := os.OpenFile(LOG_FILE, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("Ошибка записи в лог: %v\n", err)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(entry)
}

func modelKeyboard() *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	rows := [][]tele.InlineButton{}
	row := []tele.InlineButton{}
	for model := range MODELS_INFO {
		btn := tele.InlineButton{Text: model, Data: "set_" + model}
		row = append(row, btn)
		if len(row) == 2 {
			// append row and reset
			rows = append(rows, row)
			row = []tele.InlineButton{}
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	m.InlineKeyboard = rows
	return m
}

// OpenRouter request/response types
type orMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type orRequest struct {
	Model    string      `json:"model"`
	Messages []orMessage `json:"messages"`
}

type orResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Gemini request/response types
type geminiRequest struct {
	Contents []struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"contents"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func handleOpenRouter(model, prompt, apiKey string) string {
	if apiKey == "" {
		return "Ошибка: отсутствует OPENROUTER_API_KEY"
	}
	reqBody := orRequest{
		Model: model,
		Messages: []orMessage{{
			Role:    "user",
			Content: prompt,
		}},
	}
	b, _ := json.Marshal(reqBody)
	httpReq, _ := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewBuffer(b))
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Sprintf("Ошибка соединения с OpenRouter: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		text := buf.String()
		if len(text) > 200 {
			text = text[:200]
		}
		return fmt.Sprintf("Ошибка OpenRouter (%d): %s", resp.StatusCode, text)
	}
	var orResp orResponse
	if err := json.NewDecoder(resp.Body).Decode(&orResp); err != nil {
		return fmt.Sprintf("Некорректный ответ от OpenRouter: %v", err)
	}
	if len(orResp.Choices) == 0 {
		return "Некорректный ответ от OpenRouter: пустые choices"
	}
	return orResp.Choices[0].Message.Content
}

func handleGemini(prompt, apiKey string) string {
	if apiKey == "" {
		return "Ошибка: отсутствует GEMINI_API_KEY"
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key=%s", apiKey)
	req := geminiRequest{
		Contents: []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		}{
			{
				Parts: []struct {
					Text string `json:"text"`
				}{
					{Text: prompt},
				},
			},
		},
	}
	b, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest("POST", url, bytes.NewBuffer(b))
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Sprintf("Ошибка API Gemini: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "Ошибка API Gemini"
	}
	var gr geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return fmt.Sprintf("Ошибка разбора ответа Gemini: %v", err)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return "Пустой ответ от Gemini"
	}
	return gr.Candidates[0].Content.Parts[0].Text
}

func main() {
	// Load environment variables from .env in current working directory
	_ = godotenv.Load()

	token := os.Getenv("TOKEN")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	openrouterKey := os.Getenv("OPENROUTER_API_KEY")

	if token == "" {
		fmt.Println("Ошибка: переменная среды TOKEN не задана")
		return
	}

	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}
	b, err := tele.NewBot(pref)
	if err != nil {
		fmt.Printf("Не удалось создать бота: %v\n", err)
		return
	}

	// /start и /help
	b.Handle("/start", func(c tele.Context) error {
		txt := "Привет! Я мульти-модельный AI бот\nДоступные команды:\n/model - Выбрать модель\n/clear - Очистить историю"
		_ = c.Send(txt)
		logEntry(c.Sender().ID, "Command: /start", "", "")
		return nil
	})

	b.Handle("/help", func(c tele.Context) error {
		txt := "Привет! Я мульти-модельный AI бот\nДоступные команды:\n/model - Выбрать модель\n/clear - Очистить историю"
		_ = c.Send(txt)
		logEntry(c.Sender().ID, "Command: /help", "", "")
		return nil
	})

	// /model
	b.Handle("/model", func(c tele.Context) error {
		_ = c.Send("Выберите модель:", modelKeyboard())
		logEntry(c.Sender().ID, "Requested model change", "", "")
		return nil
	})

	// Обработка callback "set_<model>"
	b.Handle(tele.OnCallback, func(c tele.Context) error {
		cb := c.Callback()
		if cb == nil {
			return nil
		}
		data := cb.Data
		if strings.HasPrefix(data, "set_") {
			model := strings.TrimPrefix(data, "set_")
			mu.Lock()
			userModels[c.Sender().ID] = model
			mu.Unlock()
			_ = c.Edit(fmt.Sprintf("Модель изменена на %s", model))
			if model == "image/sora_v2" {
				_ = c.Send("Опишите изображение, которое вы хотите сгенерировать")
			}
			logEntry(c.Sender().ID, fmt.Sprintf("Model changed to %s", model), "", "")
		}
		return c.Respond()
	})

	// /clear
	b.Handle("/clear", func(c tele.Context) error {
		mu.Lock()
		memory[c.Sender().ID] = ""
		mu.Unlock()
		_ = c.Send("История очищена!")
		logEntry(c.Sender().ID, "History cleared", "", "")
		return nil
	})

	// Основной обработчик текста
	b.Handle(tele.OnText, func(c tele.Context) error {
		userID := c.Sender().ID
		mu.Lock()
		model := userModels[userID]
		if model == "" {
			model = "gpt-3.5"
		}
		history := memory[userID]
		history += "\nUser: " + c.Text()
		memory[userID] = history
		mu.Unlock()

		var response string
		info := MODELS_INFO[model]
		prompt := history

		switch info.Provider {
		case "gemini":
			response = handleGemini(prompt, geminiKey)
		case "openrouter":
			response = handleOpenRouter(info.Name, prompt, openrouterKey)
		case "selenium":
			// Send initial status to user
			_ = c.Send("Отправляю запрос в Sora. Ожидайте генерации…")
			// Pass user's text as prompt to Python Selenium runner
			cmd := exec.Command("bash", "-c", fmt.Sprintf("source venv/bin/activate && python3 sora_runner.py --prompt %q", c.Text()))
			out, err := cmd.CombinedOutput()
			if err != nil {
				response = fmt.Sprintf("Ошибка запуска Selenium: %v\n%s", err, string(out))
			} else {
				// The Python script prints markers: WAITING then DONE
				// We simply confirm completion here.
				response = "Генерация завершена"
			}
		default:
			response = "Неизвестная модель"
		}

		mu.Lock()
		memory[userID] = memory[userID] + "\nAI: " + response
		mu.Unlock()

		logEntry(userID, c.Text(), response, model)
		return c.Send(response)
	})

	b.Start()
}
