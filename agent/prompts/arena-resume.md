Продолжи уже начатую автономную разработку `JoTalbot/words`.

НЕ начинай проект заново и НЕ повторяй выполненную работу.

1. Прочитай `AGENTS.md`, `docs/AUTONOMOUS-DEVELOPMENT-MASTER.md`, `docs/TASK-PROTOCOL.md`, `docs/M0.md`, `docs/ROADMAP.md`, `docs/PRODUCT-DECISIONS.md`, `docs/ARCHITECTURE.md`, `agent/state/current.yml`.
2. Через SSH проверь фактическое состояние сервера, Git, worktrees, процессы workers, тесты, CI, Docker, Android/ADB/emulator, браузерную автоматизацию и локальные LLM/CLI.
3. Восстанови реальный task graph: completed / in progress / failed / blocked / available. Не доверяй устаревшим статусам без проверки кодом и тестами.
4. Запусти следующий максимально полезный batch. Независимые задачи выполняй параллельно, не допуская конфликтов файлов.
5. Используй локальные LLM для мелких задач, сильные модели для архитектуры, gameplay, networking, security и сложного debugging. Автоматически обнаруживай Hermes CLI, Ollama, llama.cpp, vLLM и другие доступные варианты.
6. Тестируй автоматически. При ошибке: reproduce → diagnose → regression test → fix → retest → integrate.
7. Для Android сначала используй локальный emulator. Если он недоступен/неработоспособен, автоматически найди и используй доступный и разрешённый remote/cloud/online Android emulator или device farm. Возможные варианты включают MyAndroid.org и другие найденные сервисы, но НЕ привязывайся к одному провайдеру. Предпочитай API/автоматизацию, а не ручное управление браузером. Не обходи авторизацию, CAPTCHA, лимиты или защиту провайдера.
8. Если Android временно недоступен, не останавливай проект: продолжай остальные задачи и создай/обнови Android-blocked task.
9. Используй Playwright или лучший доступный browser automation tool для project-controlled web/test environments.
10. После каждого значимого batch обновляй `agent/state/current.yml`, чтобы следующая Arena-сессия могла продолжить без человека.
11. Не проси человека выполнять команды, тесты, Git, ADB, browser или обычный debugging. Останавливайся только на настоящем human blocker: credentials/authorization, необратимое production-действие, существенное product/legal/business решение или критическое security/safety решение.
12. Перед окончанием текущей сессии обязательно сохрани состояние, commit и точку resume.

## ОБЯЗАТЕЛЬНЫЕ СТАТУСЫ

Во время работы сообщай на русском языке короткими понятными этапами, а не списком сырых команд:

`[ЭТАП] что делаешь`
`[ЗАЧЕМ] зачем`
`[ПРОГРЕСС] текущий milestone/task count или честный процент`
`[РЕЗУЛЬТАТ] что получилось`
`[ДАЛЬШЕ] что запускаешь следующим`

Показывай прогресс по `docs/ROADMAP.md` на каждом существенном цикле. Не выдумывай проценты: если точный процент невозможен, показывай, например, `M0: 7/18 задач`.

Не пиши длинный отчёт вместо работы. После каждого завершённого batch сразу выбирай и запускай следующий.

Главный цикл:

`recover → task graph → parallel batch → test → repair → integrate → roadmap/state update → next batch → repeat`

**Начинай прямо сейчас с восстановления фактического состояния и продолжай работу до release gate или настоящего human blocker.**
