package telegram

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

const taskHelp = "/task add <проверка> <отчёт> <текст> — создать задачу, например:\n" +
	"/task add 5m 1h Проверяй nginx на web-1; проблема, если сервис не active.\n" +
	"/task list, /task show <id>, /task pause <id>, /task resume <id>, /task delete <id>.\n" +
	"Лимит MVP: 5 задач на весь бот, включая задачи на паузе.\n" +
	"Без явного критерия оценка проблемы остаётся на усмотрение модели."

type taskCommand struct {
	ownerID  int64
	chatID   int64
	updateID int64
	input    string
	reply    chan taskCommandReply
}

type taskCommandReply struct {
	text string
	err  error
}

func (s *taskScheduler) command(ctx context.Context, m *message, updateID int64, input string) (string, error) {
	cmd := taskCommand{m.From.ID, m.Chat.ID, updateID, input, make(chan taskCommandReply, 1)}
	select {
	case s.commands <- cmd:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case reply := <-cmd.reply:
		return reply.text, reply.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *taskScheduler) handleCommand(cmd taskCommand) taskCommandReply {
	if !slices.Contains(s.bot.users(), cmd.ownerID) {
		return taskCommandReply{}
	}
	if err := s.reload(); err != nil {
		s.bot.log(err)
		return taskCommandReply{text: "Файл задач недоступен или некорректен. Исправь файл; он не был перезаписан."}
	}
	last := s.file.LastCommand
	if cmd.updateID != 0 && last.UpdateID == cmd.updateID && last.OwnerID == cmd.ownerID &&
		last.Input == cmd.input && s.now().Sub(last.At) < 24*time.Hour {
		return taskCommandReply{text: last.Reply}
	}
	text, changed := s.editCommand(cmd)
	if !changed {
		return taskCommandReply{text: text}
	}
	s.file.LastCommand = taskCommandCheckpoint{cmd.updateID, cmd.ownerID, cmd.input, text, s.now().UTC()}
	return taskCommandReply{text: text, err: s.save()}
}

func (s *taskScheduler) editCommand(cmd taskCommand) (string, bool) {
	parts := strings.Fields(cmd.input)
	if len(parts) < 2 {
		return taskHelp, false
	}
	switch parts[1] {
	case "add":
		if len(parts) < 5 {
			return taskHelp, false
		}
		check, report, err := taskIntervals(parts[2], parts[3])
		if err != nil {
			return err.Error(), false
		}
		if len(s.file.Tasks) >= maxScheduledTasks {
			return fmt.Sprintf("Достигнут лимит MVP: %d задач на весь бот, включая задачи на паузе. /task list — посмотреть свои задачи, /task delete <id> — удалить задачу и освободить место.", maxScheduledTasks), false
		}
		// Cut the four command words without changing whitespace inside the prompt.
		prompt := cmd.input
		for range 4 {
			prompt = strings.TrimSpace(prompt)
			index := strings.IndexFunc(prompt, unicode.IsSpace)
			if index < 0 {
				return taskHelp, false
			}
			prompt = prompt[index:]
		}
		prompt = strings.TrimSpace(prompt)
		now := s.now().UTC()
		s.revision++
		task := &scheduledTask{
			ID: newTaskID(), OwnerID: cmd.ownerID, ChatID: cmd.chatID, Prompt: prompt,
			Every: parts[2], ReportEvery: parts[3], Enabled: true, revision: s.revision,
			State: taskState{NextRun: now.Add(check), NextReport: now.Add(report), PeriodStart: now},
		}
		s.file.Tasks = append(s.file.Tasks, task)
		return fmt.Sprintf("Задача %s создана. Проверка каждые %s, сводка каждые %s.\n%s\nБез явного критерия оценка проблемы остаётся на усмотрение модели.", task.ID, task.Every, task.ReportEvery, task.Prompt), true
	case "list":
		if len(parts) != 2 {
			return taskHelp, false
		}
		var lines []string
		for _, task := range s.file.Tasks {
			if task.OwnerID == cmd.ownerID {
				status := "пауза"
				if task.Enabled {
					status = "включена"
				}
				lines = append(lines, fmt.Sprintf("%s · %s · %s / %s\n%s", task.ID, status, task.Every, task.ReportEvery, task.Prompt))
			}
		}
		if len(lines) == 0 {
			return "Задач пока нет.\n" + taskHelp, false
		}
		return strings.Join(lines, "\n\n"), false
	case "show", "pause", "resume", "delete":
		if len(parts) != 3 {
			return taskHelp, false
		}
		task := s.find(parts[2])
		if task == nil || task.OwnerID != cmd.ownerID {
			return "Задача не найдена.", false
		}
		if parts[1] == "show" {
			return fmt.Sprintf("Задача %s · включена: %t\n%s\nПроверка: %s; сводка: %s\nСледующая проверка: %s\nПоследний результат: %s\n%s", task.ID, task.Enabled, task.Prompt, task.Every, task.ReportEvery, task.State.NextRun.Format(time.RFC3339), task.State.LastResult.Status, task.State.LastResult.Summary), false
		}
		s.revision++
		task.revision = s.revision
		task.State.Running = false
		task.State.PendingNotice = ""
		if parts[1] == "delete" {
			s.file.Tasks = slices.DeleteFunc(s.file.Tasks, func(t *scheduledTask) bool { return t.ID == task.ID })
			return "Задача удалена.", true
		}
		task.Enabled = parts[1] == "resume"
		if task.Enabled {
			check, report, _ := taskIntervals(task.Every, task.ReportEvery)
			task.State.NextRun = s.now().UTC().Add(check)
			task.State.NextReport = s.now().UTC().Add(report)
			return "Задача возобновлена.", true
		}
		return "Задача на паузе.", true
	default:
		return taskHelp, false
	}
}
