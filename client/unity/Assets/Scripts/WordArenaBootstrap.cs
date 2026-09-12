using System;
using System.Collections;
using System.Collections.Generic;
using UnityEngine;

namespace Words.Client
{
    /// <summary>
    /// M1 client UX bootstrap. It now has two modes:
    ///
    /// 1. Local demo mode keeps the no-server touch affordances from Batch 11.
    /// 2. Server mode creates/connects to a match, submits only player intents,
    ///    and renders canonical snapshots/events from the authoritative Go
    ///    WebSocket protobuf protocol.
    ///
    /// The object is created at runtime and rendered with IMGUI so the minimal
    /// scene can keep building on CI without requiring a Linux/ARM64 Unity
    /// Editor session to serialize GameObjects or uGUI/EventSystem objects.
    /// </summary>
    public sealed class WordArenaBootstrap : MonoBehaviour
    {
        private const float LockSeconds = 3f;
        private const float ComboWindowSeconds = 10f;
        private static readonly string[] DemoLetters = { "T", "A", "A", "N", "D", "S", "V", "T", "G", "C", "O", "D" };
        private static readonly HashSet<string> DemoWords = new HashSet<string>(StringComparer.OrdinalIgnoreCase) { "CAT", "DOG" };

        private readonly List<CellViewModel> cells = new List<CellViewModel>();
        private readonly List<int> selected = new List<int>();
        private readonly PlayerViewModel[] players = { new PlayerViewModel("Blue"), new PlayerViewModel("Orange") };
        private readonly Queue<Action> mainThreadActions = new Queue<Action>();
        private readonly object mainThreadActionsLock = new object();
        private readonly List<PendingIntentViewModel> pendingIntents = new List<PendingIntentViewModel>();

        // Batch 16 gesture path: drag/swipe multi-cell selection state.
        private readonly List<Rect> boardCellRects = new List<Rect>();
        private readonly List<int> dragPath = new List<int>();
        private bool dragActive;
        private bool boardRectsValid;
        private bool boardRectLogged;
        private float boardScale = 1f;

        // Batch 16 production result presentation overlay state.
        private bool matchOver;
        private bool resultOverlayDismissed;
        private MatchResultSummary lastResult;

        // Batch 17A matchmaking queue state. The queue is a server-side FIFO
        // pairing; the client only enqueues, polls, and connects with the
        // seat token the server issues on match. Match authority is unchanged.
        private bool queueInProgress;
        private bool queuePolling;
        private bool queuePollActive;
        private string queueId = string.Empty;
        private string queueSummary = "Queue: idle";
        private float nextQueuePollAt;
        private ulong queueUserId;
        private string queueToken = string.Empty;

        // Batch 17B profile-aware queueing state. The profile is optional:
        // without one, queue entries stay anonymous (server default). With
        // one, finished matches fold into lifetime stats server-side.
        private string nickname = "pilot";
        private bool profileCreateInProgress;
        private bool profileStatsInProgress;
        private bool profileStatsRequested;
        private ulong profileId;
        private string profileSummary = "Profile: anonymous (queue entries anonymous)";

        // Batch 17C Sudden Death presentation state. Client-observed only:
        // the authoritative winner/tie always comes from the REST result; the
        // tiebreak word is the last accepted event this client saw.
        private string tiebreakWord = string.Empty;
        private int tiebreakSeat = -1;

        // Batch 17F: collapsible color legend for the shared board.
        private bool showLegend;

        private WordArenaNetworkClient network;
        private GUIStyle titleStyle;
        private GUIStyle bannerStyle;
        private GUIStyle hudStyle;
        private GUIStyle statusStyle;
        private GUIStyle cellStyle;
        private GUIStyle actionStyle;
        private GUIStyle inputStyle;
        private GUIStyle overlayStyle;
        private int activeSeat;
        private bool suddenDeath;
        private bool serverMode;
        private bool createInProgress;
        private bool readyCheckInProgress;
        private bool tokenRotationInProgress;
        private bool resultFetchInProgress;
        private bool terminalResultFetchRequested;
        private float lastAcceptedAt = -999f;
        private string serverUrl = "http://127.0.0.1:18080";
        private string language = "en";
        private string status = "Demo board ready. Use Create server match to bind this UI to the authoritative backend.";
        private string readySummary = "Ready: not checked";
        private string resultSummary = "Result: not fetched";
        private string predictionSummary = "Prediction: idle";
        private ulong liveMatchId;
        private ulong liveSeed;
        private uint lastServerTick;
        private uint lastStateVersion;
        private uint clientSequence;
        private string[] liveTokens = new string[2];
        // Batch 21g: the unguessable match code and the per-match read capability
        // for the live match. They are issued once, at create or at queue match,
        // and are what the result endpoint now requires.
        private string liveMatchCode = string.Empty;
        private string liveReadCapability = string.Empty;
        private ulong[] liveUserIds = new ulong[2];

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.AfterSceneLoad)]
        private static void Bootstrap()
        {
            if (FindFirstObjectByType<WordArenaBootstrap>() != null)
            {
                return;
            }

            var host = new GameObject("WordArenaBootstrap");
            DontDestroyOnLoad(host);
            host.AddComponent<WordArenaBootstrap>();
        }

        private void Awake()
        {
            ResetDemoState();
            network = new WordArenaNetworkClient();
            network.StatusChanged += message => EnqueueOnMainThread(() => SetStatus(message));
            network.SnapshotReceived += snapshot => EnqueueOnMainThread(() => ApplyServerSnapshot(snapshot));
            network.WordEventReceived += wordEvent => EnqueueOnMainThread(() => ApplyServerEvent(wordEvent));
        }

        private void OnDestroy()
        {
            if (network != null)
            {
                network.Dispose();
                network = null;
            }
        }

        private void Update()
        {
            DrainMainThreadActions();
            TickVisibleLocks();
            TickPendingIntents();
            TickQueuePolling();
        }

        // Batch 17A: poll the matchmaking entry while searching for an
        // opponent. Poll failures retry on the next interval; HTTP 410
        // (expired) stops the search with guidance.
        private void TickQueuePolling()
        {
            if (!queuePolling || queuePollActive || network == null || string.IsNullOrEmpty(queueId))
            {
                return;
            }

            if (Time.time < nextQueuePollAt)
            {
                return;
            }

            nextQueuePollAt = Time.time + 1.5f;
            StartCoroutine(PollQueueOnce());
        }

        private IEnumerator PollQueueOnce()
        {
            queuePollActive = true;
            QueueEntryResult polled = null;
            yield return StartCoroutine(network.PollQueue(serverUrl, queueId, entry => polled = entry));
            queuePollActive = false;
            HandleQueuePollResult(polled);
        }

        private void OnGUI()
        {
            EnsureStyles();
            var oldMatrix = GUI.matrix;
            var oldBackground = GUI.backgroundColor;
            var scale = Mathf.Min(Screen.width / 1080f, Screen.height / 1920f);
            if (scale <= 0f)
            {
                scale = 1f;
            }

            GUI.matrix = Matrix4x4.TRS(Vector3.zero, Quaternion.identity, new Vector3(scale, scale, 1f));
            boardScale = scale;
            var width = Screen.width / scale;
            var height = Screen.height / scale;

            GUILayout.BeginArea(new Rect(40f, 40f, width - 80f, height - 80f));
            GUILayout.Label("Word Arena", titleStyle, GUILayout.Height(72f));
            GUILayout.Label(BannerText(), bannerStyle, GUILayout.Height(58f));
            GUILayout.Label(ScoreText(), hudStyle, GUILayout.Height(48f));
            GUILayout.Label(ComboText(), hudStyle, GUILayout.Height(42f));
            DrawConnectionPanel();
            if (showLegend)
            {
                DrawLegend();
            }

            GUILayout.Space(16f);

            HandleBoardGesture();
            DrawBoard();

            GUILayout.Space(16f);
            GUILayout.Label(selected.Count == 0 ? "Tap or swipe across cells to spell a word path" : "Selected: " + CurrentWord(), hudStyle, GUILayout.Height(50f));
            GUILayout.Label(status, statusStyle, GUILayout.Height(96f));
            GUILayout.FlexibleSpace();
            DrawActions();
            GUILayout.EndArea();

            DrawResultOverlay();

            GUI.backgroundColor = oldBackground;
            GUI.matrix = oldMatrix;
        }

        private void DrawConnectionPanel()
        {
            GUILayout.Label(ConnectionText(), statusStyle, GUILayout.Height(54f));
            GUILayout.Label(readySummary + "   |   " + resultSummary, statusStyle, GUILayout.Height(54f));
            GUILayout.Label(predictionSummary, statusStyle, GUILayout.Height(46f));
            GUILayout.Label(queueSummary, statusStyle, GUILayout.Height(46f));
            GUILayout.BeginHorizontal();
            GUILayout.Label("Server", hudStyle, GUILayout.Width(130f), GUILayout.Height(48f));
            serverUrl = GUILayout.TextField(serverUrl, inputStyle, GUILayout.Height(48f));
            GUILayout.Label("Lang", hudStyle, GUILayout.Width(90f), GUILayout.Height(48f));
            language = GUILayout.TextField(language, inputStyle, GUILayout.Width(90f), GUILayout.Height(48f));
            GUILayout.EndHorizontal();
            GUILayout.BeginHorizontal();
            GUILayout.Label("Nick", hudStyle, GUILayout.Width(130f), GUILayout.Height(48f));
            nickname = GUILayout.TextField(nickname, inputStyle, GUILayout.Height(48f));
            GUI.backgroundColor = new Color32(150, 120, 200, 255);
            if (GUILayout.Button(profileCreateInProgress ? "Creating..." : "Create profile", actionStyle, GUILayout.Width(330f), GUILayout.Height(48f)))
            {
                OnCreateProfile();
            }

            GUILayout.EndHorizontal();
            GUILayout.Label(profileSummary, statusStyle, GUILayout.Height(46f));
        }

        // Batch 17F: board color legend so ownership/lock/pending states are
        // readable without prior knowledge. Presentation help only.
        private void DrawLegend()
        {
            GUILayout.Label("Legend: BLUE = seat 0 owned (bright = locked) | ORANGE = seat 1 owned (bright = locked) | YELLOW = your selection/drag or pending intent | GREEN = pending accepted | RED = pending rolled back | GREY = free. Locks expire after 3s; locked enemy cells can be Cross-Stolen. Ownership is always authoritative from server snapshots.", statusStyle, GUILayout.Height(150f));
        }

        private void OnToggleLegend()
        {
            showLegend = !showLegend;
            SetStatus(showLegend ? "Board color legend shown." : "Board color legend hidden.");
        }

        private void DrawBoard()
        {
            var columns = 4;
            var rows = Mathf.CeilToInt(cells.Count / (float)columns);
            var captureRects = Event.current == null || Event.current.type != EventType.Layout;
            var captured = new List<Rect>();
            for (var row = 0; row < rows; row++)
            {
                GUILayout.BeginHorizontal();
                for (var col = 0; col < columns; col++)
                {
                    var index = row * columns + col;
                    if (index >= cells.Count)
                    {
                        GUILayout.Space(238f);
                        if (captureRects)
                        {
                            captured.Add(Rect.zero);
                        }

                        continue;
                    }

                    var cell = cells[index];
                    GUI.backgroundColor = CellColor(cell);
                    if (GUILayout.Button(CellLabel(cell), cellStyle, GUILayout.Width(238f), GUILayout.Height(150f)))
                    {
                        // Pointer selection is owned by the Batch 16 gesture
                        // path; this click stays as keyboard/fallback input.
                        OnCellTapped(cell.CellId);
                    }

                    if (captureRects)
                    {
                        captured.Add(GUILayoutUtility.GetLastRect());
                    }
                }

                GUILayout.EndHorizontal();
                GUILayout.Space(14f);
            }

            if (captureRects && captured.Count >= cells.Count)
            {
                boardCellRects.Clear();
                boardCellRects.AddRange(captured);
                boardRectsValid = true;
                LogBoardRectOnce();
            }
        }

        // Batch 26D: publish where the board actually is. IMGUI lays the cells
        // out at runtime, so their position moves every time a panel is added
        // above them; the device smoke used a hardcoded swipe row measured
        // against an older layout, and a swipe that misses the board produces
        // no marker and reads as a broken input path. The rect is logged in the
        // same GUI space HandleBoardGesture hit-tests in, together with the
        // GUI.matrix scale as an integer permille so a caller can convert to
        // screen pixels without depending on any float formatting culture.
        private void LogBoardRectOnce()
        {
            if (boardRectLogged)
            {
                return;
            }

            var minX = float.MaxValue;
            var minY = float.MaxValue;
            var maxX = float.MinValue;
            var maxY = float.MinValue;
            var found = false;
            foreach (var r in boardCellRects)
            {
                if (r.width <= 0f || r.height <= 0f)
                {
                    continue;
                }

                found = true;
                minX = Mathf.Min(minX, r.xMin);
                minY = Mathf.Min(minY, r.yMin);
                maxX = Mathf.Max(maxX, r.xMax);
                maxY = Mathf.Max(maxY, r.yMax);
            }

            if (!found)
            {
                return;
            }

            boardRectLogged = true;
            Debug.Log("[WORDS_BOARD_RECT] x0=" + Mathf.RoundToInt(minX)
                + " y0=" + Mathf.RoundToInt(minY)
                + " x1=" + Mathf.RoundToInt(maxX)
                + " y1=" + Mathf.RoundToInt(maxY)
                + " cells=" + cells.Count
                + " scale_permille=" + Mathf.RoundToInt(boardScale * 1000f));
        }

        // Batch 16 gesture path: drag/swipe multi-cell selection. IMGUI
        // pointer/touch events are consumed here against the previous-frame
        // cell rectangles so a swipe spells a path; a single tap keeps the
        // legacy toggle behaviour. Selection stays presentational only — the
        // authoritative server validates every submitted path.
        private void HandleBoardGesture()
        {
            var evt = Event.current;
            if (evt == null || cells.Count == 0 || !boardRectsValid)
            {
                return;
            }

            if (evt.type == EventType.MouseDown)
            {
                if (evt.button != 0)
                {
                    return;
                }

                var startCell = HitTestBoardCell(BoardLocalPoint(evt.mousePosition));
                if (startCell < 0)
                {
                    return;
                }

                dragActive = true;
                dragPath.Clear();
                dragPath.Add(startCell);
                evt.Use();
            }
            else if (evt.type == EventType.MouseDrag && dragActive)
            {
                ExtendDragPath(evt.mousePosition);
                evt.Use();
            }
            else if (evt.type == EventType.MouseUp && dragActive)
            {
                dragActive = false;
                ExtendDragPath(evt.mousePosition);
                CommitDragPath();
                evt.Use();
            }
        }

        private Vector2 BoardLocalPoint(Vector2 screenPoint)
        {
            var scale = boardScale > 0f ? boardScale : 1f;
            return new Vector2(screenPoint.x / scale - 40f, screenPoint.y / scale - 40f);
        }

        private int HitTestBoardCell(Vector2 localPoint)
        {
            for (var index = 0; index < boardCellRects.Count && index < cells.Count; index++)
            {
                var rect = boardCellRects[index];
                if (rect.width > 1f && rect.height > 1f && rect.Contains(localPoint))
                {
                    return cells[index].CellId;
                }
            }

            return -1;
        }

        // Batch 17E gesture rules (ported from session B's
        // batch16-arena-swipe reference ideas, agent/COORDINATION.md):
        // drag extension follows eight-way adjacency, bridges a single
        // skipped cell when the pointer moves too fast, backtracks when the
        // pointer returns to the previous cell, and enforces a hard path cap.
        private const int BoardColumns = 4;
        private const int MaxPathCells = 12;

        private static void GridPosition(int index, out int row, out int col)
        {
            if (index < 0)
            {
                row = -10;
                col = -10;
                return;
            }

            row = index / BoardColumns;
            col = index % BoardColumns;
        }

        private static bool CellsAdjacent(int indexA, int indexB)
        {
            if (indexA < 0 || indexB < 0 || indexA == indexB)
            {
                return false;
            }

            int rowA, colA, rowB, colB;
            GridPosition(indexA, out rowA, out colA);
            GridPosition(indexB, out rowB, out colB);
            return Mathf.Abs(rowA - rowB) <= 1 && Mathf.Abs(colA - colB) <= 1;
        }

        private int IndexOfCell(int cellId)
        {
            for (var index = 0; index < cells.Count; index++)
            {
                if (cells[index].CellId == cellId)
                {
                    return index;
                }
            }

            return -1;
        }

        private bool TryBridgeCells(int fromCellId, int toCellId, out int bridgeIndex)
        {
            bridgeIndex = -1;
            var fromIndex = IndexOfCell(fromCellId);
            var toIndex = IndexOfCell(toCellId);
            if (fromIndex < 0 || toIndex < 0)
            {
                return false;
            }

            int rowFrom, colFrom, rowTo, colTo;
            GridPosition(fromIndex, out rowFrom, out colFrom);
            GridPosition(toIndex, out rowTo, out colTo);
            if (Mathf.Abs(rowTo - rowFrom) > 2 || Mathf.Abs(colTo - colFrom) > 2)
            {
                return false;
            }

            for (var index = 0; index < cells.Count; index++)
            {
                if (index == fromIndex || index == toIndex || dragPath.Contains(cells[index].CellId))
                {
                    continue;
                }

                if (CellsAdjacent(fromIndex, index) && CellsAdjacent(index, toIndex))
                {
                    bridgeIndex = index;
                    return true;
                }
            }

            return false;
        }

        private void ExtendDragPath(Vector2 screenPoint)
        {
            var cellId = HitTestBoardCell(BoardLocalPoint(screenPoint));
            if (cellId < 0)
            {
                return;
            }

            var count = dragPath.Count;
            if (count >= 2 && dragPath[count - 2] == cellId)
            {
                // Swiping back onto the previous cell undoes the last step.
                dragPath.RemoveAt(count - 1);
                return;
            }

            if (dragPath.Contains(cellId))
            {
                return;
            }

            if (count == 0)
            {
                dragPath.Add(cellId);
                return;
            }

            if (count >= MaxPathCells)
            {
                return;
            }

            var lastCellId = dragPath[count - 1];
            if (CellsAdjacent(IndexOfCell(lastCellId), IndexOfCell(cellId)))
            {
                dragPath.Add(cellId);
                return;
            }

            int bridgeIndex;
            if (count + 1 < MaxPathCells && TryBridgeCells(lastCellId, cellId, out bridgeIndex))
            {
                dragPath.Add(cells[bridgeIndex].CellId);
                dragPath.Add(cellId);
            }
        }

        private void CommitDragPath()
        {
            if (dragPath.Count <= 1)
            {
                if (dragPath.Count == 1)
                {
                    OnCellTapped(dragPath[0]);
                }
            }
            else
            {
                selected.Clear();
                selected.AddRange(dragPath);
                SetStatus("Swipe path '" + CurrentWord() + "' selected (" + selected.Count + " cells). Send when ready.");
                // Batch 17E marker: lets the hosted-emulator smoke verify the
                // gesture path with `adb shell input swipe` + logcat grep.
                Debug.Log("[WORDS_SWIPE] cells=" + selected.Count + " word=" + CurrentWord());
            }

            dragPath.Clear();
        }

        private void DrawActions()
        {
            GUILayout.BeginHorizontal();
            DrawActionButton(serverMode ? "Send selected" : "Claim selected", OnClaimSelected, new Color32(63, 137, 255, 255));
            DrawActionButton("Switch seat", OnSwitchSeat, new Color32(255, 142, 63, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton("Clear path", OnClearSelection, new Color32(90, 105, 128, 255));
            DrawActionButton("Sudden Death", OnToggleSuddenDeath, new Color32(155, 84, 255, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton(createInProgress ? "Creating..." : "Create server match", OnCreateServerMatch, new Color32(54, 172, 118, 255));
            DrawActionButton(readyCheckInProgress ? "Checking ready..." : "Ready check", OnReadyCheck, new Color32(72, 150, 170, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton(serverMode ? "Reconnect seat" : "Reset demo", serverMode ? (Action)ReconnectActiveSeat : ResetDemoState, new Color32(96, 112, 146, 255));
            DrawActionButton(tokenRotationInProgress ? "Rotating..." : "Rotate token", OnRotateActiveToken, new Color32(190, 128, 60, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton(resultFetchInProgress ? "Fetching result..." : "Fetch result", OnFetchResult, new Color32(80, 120, 210, 255));
            DrawActionButton("Local demo reset", ResetDemoState, new Color32(90, 105, 128, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton(QueueButtonLabel(), OnFindMatchQueue, new Color32(64, 160, 120, 255));
            DrawActionButton("Stop searching", OnStopQueueSearch, new Color32(90, 105, 128, 255));
            GUILayout.EndHorizontal();
            GUILayout.Space(12f);
            GUILayout.BeginHorizontal();
            DrawActionButton(showLegend ? "Hide color legend" : "Show color legend", OnToggleLegend, new Color32(70, 100, 150, 255));
            GUILayout.EndHorizontal();
        }

        private string QueueButtonLabel()
        {
            if (queueInProgress)
            {
                return "Queueing...";
            }

            return queuePolling ? "Searching for opponent..." : "Find match (queue)";
        }

        // Batch 16 production result presentation overlay: a centered,
        // dismissable panel rendered once a terminal snapshot or an
        // authoritative REST result is known. The headline prefers the
        // authoritative REST record and falls back to snapshot scores until
        // the result fetch completes.
        private void DrawResultOverlay()
        {
            if ((!matchOver && lastResult == null) || resultOverlayDismissed)
            {
                return;
            }

            var areaWidth = Screen.width / boardScale;
            var areaHeight = Screen.height / boardScale;
            var width = Mathf.Min(900f, areaWidth - 40f);
            var height = Mathf.Min(620f, areaHeight - 60f);
            var rect = new Rect(Mathf.Max(20f, (areaWidth - width) / 2f), Mathf.Max(20f, (areaHeight - height) / 2.6f), width, height);
            GUI.Box(rect, string.Empty, overlayStyle);
            GUILayout.BeginArea(new Rect(rect.x + 28f, rect.y + 20f, rect.width - 56f, rect.height - 40f));
            GUILayout.Label("MATCH OVER", titleStyle, GUILayout.Height(70f));
            GUILayout.Label(ResultHeadline(), bannerStyle, GUILayout.Height(58f));
            GUILayout.Label(ScoreText(), hudStyle, GUILayout.Height(48f));
            GUILayout.Label(ResultSourceText(), statusStyle, GUILayout.Height(120f));
            GUILayout.Space(14f);
            GUILayout.BeginHorizontal();
            GUI.backgroundColor = new Color32(54, 172, 118, 255);
            if (GUILayout.Button(createInProgress ? "Creating..." : "Play again (new server match)", actionStyle, GUILayout.Height(80f)))
            {
                OnCreateServerMatch();
            }

            GUI.backgroundColor = new Color32(90, 105, 128, 255);
            if (GUILayout.Button(resultFetchInProgress ? "Refreshing result..." : "Refresh result", actionStyle, GUILayout.Height(80f)))
            {
                OnFetchResult();
            }

            GUI.backgroundColor = new Color32(96, 112, 146, 255);
            if (GUILayout.Button("Dismiss", actionStyle, GUILayout.Height(80f)))
            {
                resultOverlayDismissed = true;
            }

            GUILayout.EndHorizontal();
            GUILayout.EndArea();
        }

        private string ResultHeadline()
        {
            // Batch 17C: Sudden Death matches get a tiebreak-framed headline.
            var prefix = suddenDeath ? "SUDDEN DEATH — " : string.Empty;
            if (lastResult != null && lastResult.Success)
            {
                if (lastResult.IsTie)
                {
                    var tieText = suddenDeath && string.IsNullOrEmpty(tiebreakWord) ? "no accepted tiebreak word — " : string.Empty;
                    return prefix + tieText + "DRAW — final " + ScoreLine(lastResult.Scores);
                }

                var winnerSeat = Mathf.Clamp(lastResult.WinnerSeat, 0, players.Length - 1);
                return prefix + players[winnerSeat].DisplayName + " WINS — final " + ScoreLine(lastResult.Scores);
            }

            if (players[0].Score == players[1].Score)
            {
                return prefix + "DRAW (awaiting authoritative result)";
            }

            var leader = players[0].Score > players[1].Score ? players[0] : players[1];
            return prefix + leader.DisplayName + " LEADS " + players[0].Score + ":" + players[1].Score + " (awaiting authoritative result)";
        }

        private string ScoreLine(int[] scores)
        {
            if (scores != null && scores.Length >= 2)
            {
                return scores[0] + ":" + scores[1];
            }

            return players[0].Score + ":" + players[1].Score;
        }

        private string ResultSourceText()
        {
            var source = lastResult != null && lastResult.Success
                ? "Authoritative REST result: " + lastResult.DisplayText
                : "Snapshot-derived preview. The REST result endpoint is the authoritative terminal record.";
            var mode = suddenDeath ? "Sudden Death tiebreak armed" : "standard scoring";
            var text = source + "\nMatch " + liveMatchId + " | seed " + liveSeed + " | " + mode;
            if (suddenDeath && !string.IsNullOrEmpty(tiebreakWord) && tiebreakSeat >= 0 && tiebreakSeat < players.Length)
            {
                text += "\nTiebreak word (client-observed): '" + tiebreakWord + "' by " + players[tiebreakSeat].DisplayName;
            }

            return text;
        }

        private void DrawActionButton(string label, Action action, Color32 color)
        {
            GUI.backgroundColor = color;
            if (GUILayout.Button(label, actionStyle, GUILayout.Height(74f)))
            {
                action();
            }
        }

        private void OnCellTapped(int cellId)
        {
            if (selected.Contains(cellId))
            {
                selected.Remove(cellId);
            }
            else
            {
                selected.Add(cellId);
            }
        }

        private void OnClaimSelected()
        {
            if (serverMode)
            {
                SubmitSelectedToServer();
                return;
            }

            ApplyLocalDemoClaim();
        }

        private void SubmitSelectedToServer()
        {
            if (liveMatchId == 0)
            {
                SetStatus("No live match id. Create a server match first.");
                return;
            }

            if (network == null || !network.IsConnected)
            {
                SetStatus("Not connected. Reconnecting active seat before submit.");
                ReconnectActiveSeat();
                return;
            }

            if (selected.Count < 3)
            {
                SetStatus("Select at least three cells. The server will validate the real word path.");
                return;
            }

            clientSequence++;
            var submitCells = selected.ToArray();
            var submittedWord = WordForCells(submitCells);
            selected.Clear();
            pendingIntents.Add(new PendingIntentViewModel(clientSequence, submitCells, submittedWord, activeSeat, Time.time, lastStateVersion));
            predictionSummary = "Prediction: pending seq=" + clientSequence + " '" + submittedWord + "' (visual only)";
            network.SubmitWord(liveMatchId, clientSequence, submitCells);
            SetStatus("Submitted intent #" + clientSequence + " for '" + submittedWord + "'; waiting for authoritative event/snapshot.");
        }

        private void ApplyLocalDemoClaim()
        {
            var word = CurrentWord();
            if (word.Length < 3)
            {
                SetStatus("Select at least three cells. The server will validate the real word path.");
                return;
            }

            if (!DemoWords.Contains(word))
            {
                SetStatus("'" + word + "' rejected in local UX demo. Production validity is server-side only.");
                selected.Clear();
                return;
            }

            var stole = false;
            for (var index = 0; index < selected.Count; index++)
            {
                var cell = FindCell(selected[index]);
                if (cell == null)
                {
                    continue;
                }

                if (cell.Locked && cell.OwnerSeat != activeSeat)
                {
                    SetStatus("Cell " + cell.CellId + " is locked for " + cell.LockRemaining.ToString("0.0") + "s; wait before a Cross-Steal.");
                    return;
                }
            }

            for (var index = 0; index < selected.Count; index++)
            {
                var cell = FindCell(selected[index]);
                if (cell == null)
                {
                    continue;
                }

                if (cell.OwnerSeat >= 0 && cell.OwnerSeat != activeSeat)
                {
                    stole = true;
                }

                cell.OwnerSeat = activeSeat;
                cell.Locked = true;
                cell.LockRemaining = LockSeconds;
            }

            ApplyDemoScore(word, stole);
            selected.Clear();
            SetStatus(stole
                ? players[activeSeat].Name + " cross-stole '" + word + "'. Server event will be authoritative in live mode."
                : players[activeSeat].Name + " claimed '" + word + "' and locked the cells for 3s.");
        }

        private void OnSwitchSeat()
        {
            activeSeat = 1 - activeSeat;
            selected.Clear();
            if (serverMode)
            {
                ReconnectActiveSeat();
                return;
            }

            SetStatus("Active seat: " + players[activeSeat].Name + ". Shared board remains identical for both players.");
        }

        private void OnClearSelection()
        {
            selected.Clear();
            SetStatus("Selection cleared.");
        }

        private void OnToggleSuddenDeath()
        {
            suddenDeath = !suddenDeath;
            SetStatus(suddenDeath
                ? "Sudden Death presentation on: first accepted server word wins the tiebreak."
                : "Sudden Death presentation off: tied matches render as draws.");
        }


        // Batch 17A: matchmaking queue handlers. The client enqueues
        // anonymously (player_id=0 until profiles are bound), polls the entry
        // while waiting, and on "matched" connects with the server-issued seat
        // token. The server remains the only authority for pairing and seats.
        private void OnFindMatchQueue()
        {
            if (queueInProgress || queuePolling || network == null)
            {
                return;
            }

            queueInProgress = true;
            queueSummary = "Queue: joining" + (profileId != 0 ? " as profile #" + profileId : " anonymously") + "...";
            StartCoroutine(network.EnqueueQueue(serverUrl, SanitizedLanguage(), profileId, HandleEnqueueResult));
        }

        // Batch 17B: profile handlers. CreatePlayer registers a durable
        // identity; queueing with its id folds finished matches into the
        // lifetime stats that the server computes (docs/M1-PERSISTENCE.md).
        private void OnCreateProfile()
        {
            if (profileCreateInProgress || network == null)
            {
                return;
            }

            var nick = nickname == null ? string.Empty : nickname.Trim();
            if (nick.Length == 0 || nick.Length > 32)
            {
                SetStatus("Nickname must be 1..32 characters.");
                return;
            }

            profileCreateInProgress = true;
            StartCoroutine(network.CreatePlayer(serverUrl, nick, SanitizedLanguage(), HandleCreatePlayerResult));
        }

        private void HandleCreatePlayerResult(PlayerProfileResult profile)
        {
            profileCreateInProgress = false;
            if (profile == null || !profile.Success)
            {
                profileSummary = "Profile: " + (profile == null ? "create failed" : profile.Error);
                SetStatus(profileSummary);
                return;
            }

            profileId = profile.Id;
            profileSummary = "Profile: " + profile.Nickname + " #" + profile.Id + " — " + profile.StatsText;
            SetStatus(profileSummary + ". Queue entries now carry this profile.");
        }

        private void FetchProfileStatsOnce()
        {
            if (profileId == 0 || profileStatsInProgress || profileStatsRequested || network == null)
            {
                return;
            }

            profileStatsRequested = true;
            profileStatsInProgress = true;
            StartCoroutine(network.GetPlayer(serverUrl, profileId, HandleProfileStats));
        }

        private void HandleProfileStats(PlayerProfileResult profile)
        {
            profileStatsInProgress = false;
            if (profile == null || !profile.Success)
            {
                return;
            }

            profileSummary = "Profile: " + profile.Nickname + " #" + profile.Id + " — " + profile.StatsText;
        }

        private void OnStopQueueSearch()
        {
            queuePolling = false;
            queuePollActive = false;
            queueSummary = "Queue: stopped (server entry expires on its own TTL)";
            SetStatus(queueSummary);
        }

        private void HandleEnqueueResult(QueueEntryResult entry)
        {
            queueInProgress = false;
            if (entry == null || !entry.Success)
            {
                queueSummary = "Queue: " + (entry == null ? "enqueue failed" : entry.Error);
                SetStatus(queueSummary);
                return;
            }

            queueId = entry.QueueId;
            if (entry.IsMatched)
            {
                StartQueuedMatch(entry);
                return;
            }

            queuePolling = true;
            nextQueuePollAt = 0f;
            queueSummary = "Queue: waiting for opponent (" + queueId + ")";
            SetStatus(queueSummary);
        }

        private void HandleQueuePollResult(QueueEntryResult entry)
        {
            if (!queuePolling)
            {
                return;
            }

            if (entry == null || !entry.Success)
            {
                if (entry != null && entry.HttpStatus == 410)
                {
                    queuePolling = false;
                    queueSummary = "Queue: entry expired — press Find match to requeue";
                    SetStatus(queueSummary);
                }
                else
                {
                    queueSummary = "Queue: poll failed (" + (entry == null ? "no response" : entry.Error) + "); retrying";
                }

                return;
            }

            if (entry.IsMatched)
            {
                StartQueuedMatch(entry);
                return;
            }

            queueSummary = "Queue: waiting for opponent (" + queueId + ")";
        }

        private void StartQueuedMatch(QueueEntryResult entry)
        {
            queuePolling = false;
            queuePollActive = false;
            queueSummary = "Queue: matched into match " + entry.MatchId;

            serverMode = true;
            liveMatchId = entry.MatchId;
            liveSeed = entry.Seed;
            liveMatchCode = entry.MatchCode;
            liveReadCapability = entry.ReadCapability;
            liveTokens = new string[2];
            liveUserIds = new ulong[2];
            queueToken = entry.Token;
            queueUserId = entry.UserId;
            activeSeat = 0;
            clientSequence = 0;
            terminalResultFetchRequested = false;
            matchOver = false;
            resultOverlayDismissed = false;
            lastResult = null;
            tiebreakWord = string.Empty;
            tiebreakSeat = -1;
            profileStatsRequested = false;
            selected.Clear();
            pendingIntents.Clear();
            resultSummary = "Result: pending for match " + liveMatchId;

            network.Connect(serverUrl, liveMatchId, queueToken);
            SetStatus("Matched via queue into match " + liveMatchId + "; connecting with the issued seat token.");
        }

        private void OnReadyCheck()
        {
            if (readyCheckInProgress || network == null)
            {
                return;
            }

            readyCheckInProgress = true;
            StartCoroutine(network.CheckReadyz(serverUrl, HandleReadyzResult));
        }

        private void HandleReadyzResult(ReadyzResult result)
        {
            readyCheckInProgress = false;
            readySummary = result == null ? "Ready: check failed" : "Ready: " + result.DisplayText;
            SetStatus(readySummary);
        }

        private void OnRotateActiveToken()
        {
            if (!serverMode || liveMatchId == 0 || liveTokens == null || activeSeat < 0 || activeSeat >= liveTokens.Length || string.IsNullOrEmpty(liveTokens[activeSeat]))
            {
                SetStatus("No active server seat to rotate. Create a server match first.");
                return;
            }

            if (tokenRotationInProgress || network == null)
            {
                return;
            }

            tokenRotationInProgress = true;
            StartCoroutine(network.RotateSeatToken(serverUrl, liveMatchId, liveTokens[activeSeat], HandleRotateTokenResult));
        }

        private void HandleRotateTokenResult(RotateTokenResult result)
        {
            tokenRotationInProgress = false;
            if (result == null || !result.Success)
            {
                SetStatus(result == null ? "Token rotation failed." : result.Error);
                return;
            }

            if (result.Seat >= 0 && result.Seat < liveTokens.Length)
            {
                liveTokens[result.Seat] = result.Token;
            }

            if (result.Seat >= 0 && result.Seat < liveUserIds.Length && result.UserId != 0)
            {
                liveUserIds[result.Seat] = result.UserId;
                players[result.Seat].UserId = result.UserId;
            }

            SetStatus("Rotated " + players[Mathf.Clamp(result.Seat, 0, players.Length - 1)].DisplayName + " session token; reconnecting with fresh credentials.");
            if (result.Seat == activeSeat)
            {
                ReconnectActiveSeat();
            }
        }

        private void OnFetchResult()
        {
            if (!serverMode || liveMatchId == 0)
            {
                SetStatus("No server match result to fetch yet.");
                return;
            }

            FetchResultOnce(false);
        }

        private void FetchResultOnce(bool terminalSnapshot)
        {
            if (resultFetchInProgress || network == null || liveMatchId == 0)
            {
                return;
            }

            if (terminalSnapshot && terminalResultFetchRequested)
            {
                return;
            }

            terminalResultFetchRequested = terminalSnapshot || terminalResultFetchRequested;
            resultFetchInProgress = true;
            StartCoroutine(network.FetchResult(serverUrl, liveMatchId, liveMatchCode, liveReadCapability, HandleMatchResult));
        }

        private void HandleMatchResult(MatchResultSummary result)
        {
            resultFetchInProgress = false;
            resultSummary = result == null ? "Result: fetch failed" : "Result: " + result.DisplayText;
            if (result != null && result.Success)
            {
                lastResult = result;
                matchOver = matchOver || result.Over;
                resultOverlayDismissed = false;
            }

            SetStatus(resultSummary);
        }

        private void OnCreateServerMatch()
        {
            if (createInProgress || network == null)
            {
                return;
            }

            createInProgress = true;
            selected.Clear();
            StartCoroutine(network.CreateMatch(serverUrl, SanitizedLanguage(), suddenDeath, HandleCreateMatchResult));
        }

        private void HandleCreateMatchResult(CreateMatchResult result)
        {
            createInProgress = false;
            if (result == null || !result.Success)
            {
                SetStatus(result == null ? "Create match failed." : result.Error);
                return;
            }

            serverMode = true;
            liveMatchId = result.MatchId;
            liveSeed = result.Seed;
            liveMatchCode = result.MatchCode;
            liveReadCapability = result.ReadCapability;
            suddenDeath = result.SuddenDeath;
            liveTokens = result.Tokens;
            liveUserIds = result.UserIds;
            for (var index = 0; index < players.Length && index < liveUserIds.Length; index++)
            {
                players[index].UserId = liveUserIds[index];
            }

            activeSeat = 0;
            clientSequence = 0;
            terminalResultFetchRequested = false;
            matchOver = false;
            resultOverlayDismissed = false;
            lastResult = null;
            tiebreakWord = string.Empty;
            tiebreakSeat = -1;
            profileStatsRequested = false;
            queuePolling = false;
            queuePollActive = false;
            queueToken = string.Empty;
            queueUserId = 0;
            queueSummary = "Queue: idle";
            resultSummary = "Result: pending for match " + liveMatchId;
            ReconnectActiveSeat();
        }

        private void ReconnectActiveSeat()
        {
            if (network == null || liveTokens == null || activeSeat < 0 || activeSeat >= liveTokens.Length || string.IsNullOrEmpty(liveTokens[activeSeat]))
            {
                // Batch 17A: queue-created matches bind the seat lazily from
                // snapshots; until then the queue-issued token reconnects.
                if (!string.IsNullOrEmpty(queueToken) && liveMatchId != 0)
                {
                    network.Connect(serverUrl, liveMatchId, queueToken);
                    SetStatus("Reconnecting with the queue-issued seat token for match " + liveMatchId + ".");
                    return;
                }

                SetStatus("No token for active seat. Create a server match first.");
                return;
            }

            network.Connect(serverUrl, liveMatchId, liveTokens[activeSeat]);
            SetStatus("Connecting " + players[activeSeat].Name + " to match " + liveMatchId + ".");
        }

        private void ApplyServerSnapshot(WordArenaSnapshot snapshot)
        {
            if (snapshot == null)
            {
                return;
            }

            serverMode = true;
            liveMatchId = snapshot.MatchId == 0 ? liveMatchId : snapshot.MatchId;
            lastServerTick = snapshot.ServerTick;
            lastStateVersion = snapshot.StateVersion;

            for (var index = 0; index < snapshot.Players.Count && index < players.Length; index++)
            {
                var player = snapshot.Players[index];
                players[index].UserId = player.UserId;
                if (index < liveUserIds.Length && liveUserIds[index] == 0)
                {
                    liveUserIds[index] = player.UserId;
                }

                players[index].Score = (int)player.Score;
                players[index].Combo = Mathf.Max(1f, player.ComboMultiplier);
                players[index].Eliminated = player.IsEliminated;
                players[index].Rank = (int)player.RankPosition;
            }

            // Batch 17A: a queue-issued token carries only this seat's user
            // id, so the seat index is derived from the authoritative snapshot
            // once both user ids are known. Until bound, the queued token is
            // the reconnect credential.
            if (queueUserId != 0)
            {
                var boundSeat = SeatForUser(queueUserId);
                if (boundSeat >= 0)
                {
                    activeSeat = boundSeat;
                    if (boundSeat < liveUserIds.Length)
                    {
                        liveUserIds[boundSeat] = queueUserId;
                    }

                    if (boundSeat < liveTokens.Length)
                    {
                        liveTokens[boundSeat] = queueToken;
                    }

                    queueUserId = 0;
                    SetStatus("Queue seat bound: " + players[boundSeat].DisplayName + " in match " + liveMatchId + ".");
                }
            }

            var ordered = new List<WordArenaBoardCell>(snapshot.Cells);
            ordered.Sort((left, right) => left.CellId.CompareTo(right.CellId));
            cells.Clear();
            for (var index = 0; index < ordered.Count; index++)
            {
                var remote = ordered[index];
                var cell = new CellViewModel((int)remote.CellId, remote.Letter);
                cell.OwnerSeat = SeatForUser(remote.OwnerUserId);
                cell.Locked = remote.IsLocked;
                cell.LockRemaining = remote.LockRemainingMs / 1000f;
                cells.Add(cell);
            }

            ReconcilePendingWithSnapshot(snapshot.StateVersion);

            if (snapshot.Over)
            {
                matchOver = true;
                resultOverlayDismissed = false;
                SetStatus("Match over. Final authoritative score: " + ScoreText());
                FetchResultOnce(true);
                // Batch 17B: refresh lifetime stats once the server has folded
                // the finished match into the bound profile.
                FetchProfileStatsOnce();
            }
        }

        private void ApplyServerEvent(WordArenaValidatedEvent wordEvent)
        {
            if (wordEvent == null)
            {
                return;
            }

            lastServerTick = wordEvent.ServerTick;
            if (wordEvent.StateVersion > lastStateVersion)
            {
                lastStateVersion = wordEvent.StateVersion;
            }

            var seat = SeatForUser(wordEvent.UserId);
            if (seat >= 0 && seat < players.Length)
            {
                players[seat].Score = (int)wordEvent.TotalScore;
                players[seat].Combo = Mathf.Max(1f, wordEvent.ComboMultiplier);
            }

            // Batch 17C: remember the last accepted word for the Sudden Death
            // tiebreak line on the result overlay (presentational only).
            if (wordEvent.Accepted && seat >= 0 && seat < players.Length)
            {
                tiebreakWord = wordEvent.NormalizedWord;
                tiebreakSeat = seat;
            }

            ReconcilePendingWithEvent(wordEvent, seat);

            SetStatus("Server event #" + wordEvent.EventId + " seq=" + wordEvent.ClientSequence + ": "
                + ResultText(wordEvent.Result) + " '" + wordEvent.NormalizedWord + "'"
                + " +" + wordEvent.ScoreAdded + (wordEvent.IsSteal ? " Cross-Steal" : string.Empty));
        }


        private void ReconcilePendingWithEvent(WordArenaValidatedEvent wordEvent, int eventSeat)
        {
            var pending = FindPendingForEvent(wordEvent, eventSeat);
            if (pending == null)
            {
                return;
            }

            pending.Resolved = true;
            pending.Accepted = wordEvent.Accepted;
            pending.Rejected = !wordEvent.Accepted;
            pending.ResolvedAt = Time.time;
            pending.StateVersion = wordEvent.StateVersion;
            predictionSummary = wordEvent.Accepted
                ? "Prediction reconciled: seq=" + pending.Sequence + " accepted by server"
                : "Prediction rolled back: seq=" + pending.Sequence + " " + ResultText(wordEvent.Result);
        }

        private void ReconcilePendingWithSnapshot(uint snapshotVersion)
        {
            var removed = 0;
            var now = Time.time;
            for (var index = pendingIntents.Count - 1; index >= 0; index--)
            {
                var pending = pendingIntents[index];
                if (pending.Resolved && now - pending.ResolvedAt >= 0.8f)
                {
                    pendingIntents.RemoveAt(index);
                    removed++;
                    continue;
                }

                if (!pending.Resolved && snapshotVersion >= pending.SubmittedAtStateVersion && now - pending.SubmittedAt >= 1.5f)
                {
                    pendingIntents.RemoveAt(index);
                    removed++;
                }
            }

            if (removed > 0)
            {
                predictionSummary = "Prediction: canonical snapshot reconciled " + removed + " pending overlay(s)";
            }
            else if (pendingIntents.Count == 0)
            {
                predictionSummary = "Prediction: idle; canonical version=" + snapshotVersion;
            }
        }

        private void TickPendingIntents()
        {
            var now = Time.time;
            for (var index = pendingIntents.Count - 1; index >= 0; index--)
            {
                var pending = pendingIntents[index];
                if (pending.Resolved && now - pending.ResolvedAt >= 1.2f)
                {
                    pendingIntents.RemoveAt(index);
                }
                else if (!pending.Resolved && now - pending.SubmittedAt >= 8f)
                {
                    predictionSummary = "Prediction: seq=" + pending.Sequence + " still pending; waiting for server snapshot/event";
                }
            }
        }

        private PendingIntentViewModel FindPendingForEvent(WordArenaValidatedEvent wordEvent, int eventSeat)
        {
            if (wordEvent == null || wordEvent.ClientSequence == 0)
            {
                return null;
            }

            for (var index = 0; index < pendingIntents.Count; index++)
            {
                var pending = pendingIntents[index];
                if (pending.Sequence != wordEvent.ClientSequence)
                {
                    continue;
                }

                if (eventSeat >= 0 && pending.Seat != eventSeat)
                {
                    continue;
                }

                return pending;
            }

            return null;
        }

        private PendingIntentViewModel PendingForCell(int cellId)
        {
            for (var index = pendingIntents.Count - 1; index >= 0; index--)
            {
                var pending = pendingIntents[index];
                if (pending.ContainsCell(cellId))
                {
                    return pending;
                }
            }

            return null;
        }

        private void ApplyDemoScore(string word, bool stole)
        {
            var now = Time.time;
            var player = players[activeSeat];
            player.Combo = now - lastAcceptedAt <= ComboWindowSeconds ? player.Combo + 1f : 1f;
            lastAcceptedAt = now;

            var comboBonus = Mathf.Max(0, Mathf.RoundToInt(player.Combo) - 1);
            var stealBonus = stole ? 2 : 0;
            var suddenBonus = suddenDeath ? 1 : 0;
            player.Score += word.Length + comboBonus + stealBonus + suddenBonus;
        }

        private void TickVisibleLocks()
        {
            var changed = false;
            for (var index = 0; index < cells.Count; index++)
            {
                var cell = cells[index];
                if (!cell.Locked)
                {
                    continue;
                }

                cell.LockRemaining = Mathf.Max(0f, cell.LockRemaining - Time.deltaTime);
                if (cell.LockRemaining <= 0f)
                {
                    cell.Locked = false;
                    changed = true;
                }
            }

            if (changed && !serverMode)
            {
                SetStatus("Locks expired: owned cells can now be cross-stolen in the demo.");
            }
        }

        private string CurrentWord()
        {
            var result = string.Empty;
            for (var index = 0; index < selected.Count; index++)
            {
                var cell = FindCell(selected[index]);
                if (cell != null)
                {
                    result += cell.Letter;
                }
            }

            return result;
        }

        private string WordForCells(int[] cellIds)
        {
            var result = string.Empty;
            if (cellIds == null)
            {
                return result;
            }

            for (var index = 0; index < cellIds.Length; index++)
            {
                var cell = FindCell(cellIds[index]);
                if (cell != null)
                {
                    result += cell.Letter;
                }
            }

            return result;
        }

        private string CellLabel(CellViewModel cell)
        {
            var owner = cell.OwnerSeat < 0 ? "Free" : players[cell.OwnerSeat].Name;
            var lockText = cell.Locked ? "\nLOCK " + cell.LockRemaining.ToString("0.0") + "s" : string.Empty;
            var selectedIndex = selected.IndexOf(cell.CellId);
            var selectedText = selectedIndex >= 0 ? "\nSEL #" + (selectedIndex + 1) : string.Empty;
            var dragIndex = dragActive ? dragPath.IndexOf(cell.CellId) : -1;
            var dragText = dragIndex >= 0 ? "\nDRAG #" + (dragIndex + 1) : string.Empty;
            var pending = PendingForCell(cell.CellId);
            var pendingText = pending == null ? string.Empty : "\nPENDING #" + pending.Sequence;
            return cell.Letter + "\n" + owner + lockText + selectedText + dragText + pendingText;
        }

        private Color32 CellColor(CellViewModel cell)
        {
            if (selected.Contains(cell.CellId))
            {
                return new Color32(248, 210, 88, 255);
            }

            var pending = PendingForCell(cell.CellId);
            if (pending != null)
            {
                return pending.Accepted ? new Color32(108, 212, 132, 255) : pending.Rejected ? new Color32(214, 92, 92, 255) : new Color32(248, 210, 88, 255);
            }

            if (cell.OwnerSeat == 0)
            {
                return cell.Locked ? new Color32(55, 127, 230, 255) : new Color32(42, 93, 166, 255);
            }

            if (cell.OwnerSeat == 1)
            {
                return cell.Locked ? new Color32(232, 119, 50, 255) : new Color32(166, 83, 38, 255);
            }

            return new Color32(54, 67, 91, 255);
        }

        private string BannerText()
        {
            if (serverMode)
            {
                return suddenDeath ? "LIVE SERVER — SUDDEN DEATH ENABLED" : "LIVE SERVER — canonical snapshots/events";
            }

            return suddenDeath ? "DEMO SUDDEN DEATH — first accepted server word wins" : "Shared board UX prototype";
        }

        private string ScoreText()
        {
            return players[0].DisplayName + " " + players[0].Score + "  —  " + players[1].Score + " " + players[1].DisplayName
                + "   | active: " + players[activeSeat].DisplayName;
        }

        private string ComboText()
        {
            return "Combo: " + players[0].DisplayName + " x" + Mathf.Max(1f, players[0].Combo).ToString("0.##")
                + " / " + players[1].DisplayName + " x" + Mathf.Max(1f, players[1].Combo).ToString("0.##");
        }

        private string ConnectionText()
        {
            if (!serverMode)
            {
                return "Mode: local presentation demo. Server mode uses REST create + binary protobuf WebSocket.";
            }

            var connected = network != null && network.IsConnected ? "connected" : "disconnected";
            return "Mode: server " + connected + " | match=" + liveMatchId + " seed=" + liveSeed
                + " tick=" + lastServerTick + " version=" + lastStateVersion;
        }

        private string ResultText(WordArenaWordResult result)
        {
            switch (result)
            {
                case WordArenaWordResult.Accepted:
                    return "accepted";
                case WordArenaWordResult.RejectedNotInDictionary:
                    return "rejected: not in dictionary";
                case WordArenaWordResult.AlreadyClaimed:
                    return "rejected: already claimed";
                case WordArenaWordResult.BlockedByRule:
                    return "blocked by rule";
                case WordArenaWordResult.InvalidInput:
                    return "invalid input";
                case WordArenaWordResult.MatchNotActive:
                    return "match not active";
                default:
                    return "unspecified";
            }
        }

        private string SanitizedLanguage()
        {
            var value = string.IsNullOrEmpty(language) ? "en" : language.Trim().ToLowerInvariant();
            if (value != "en" && value != "ru" && value != "uk")
            {
                return "en";
            }

            return value;
        }

        private int SeatForUser(ulong userId)
        {
            if (userId == 0)
            {
                return -1;
            }

            for (var index = 0; index < liveUserIds.Length; index++)
            {
                if (liveUserIds[index] == userId)
                {
                    return index;
                }
            }

            for (var index = 0; index < players.Length; index++)
            {
                if (players[index].UserId == userId)
                {
                    return index;
                }
            }

            return -1;
        }

        private CellViewModel FindCell(int cellId)
        {
            for (var index = 0; index < cells.Count; index++)
            {
                if (cells[index].CellId == cellId)
                {
                    return cells[index];
                }
            }

            return null;
        }

        private void ResetDemoState()
        {
            if (network != null)
            {
                network.Disconnect();
            }

            serverMode = false;
            createInProgress = false;
            readyCheckInProgress = false;
            tokenRotationInProgress = false;
            resultFetchInProgress = false;
            terminalResultFetchRequested = false;
            liveMatchId = 0;
            liveSeed = 0;
            liveMatchCode = string.Empty;
            liveReadCapability = string.Empty;
            lastServerTick = 0;
            lastStateVersion = 0;
            clientSequence = 0;
            liveTokens = new string[2];
            liveUserIds = new ulong[2];
            activeSeat = 0;
            selected.Clear();
            pendingIntents.Clear();
            dragPath.Clear();
            dragActive = false;
            boardRectsValid = false;
            boardCellRects.Clear();
            matchOver = false;
            resultOverlayDismissed = false;
            lastResult = null;
            tiebreakWord = string.Empty;
            tiebreakSeat = -1;
            queueInProgress = false;
            queuePolling = false;
            queuePollActive = false;
            queueId = string.Empty;
            queueToken = string.Empty;
            queueUserId = 0;
            queueSummary = "Queue: idle";
            cells.Clear();
            for (var index = 0; index < DemoLetters.Length; index++)
            {
                cells.Add(new CellViewModel(index, DemoLetters[index]));
            }

            players[0].Reset("Blue");
            players[1].Reset("Orange");
            readySummary = "Ready: not checked";
            resultSummary = "Result: not fetched";
            predictionSummary = "Prediction: idle";
            status = "Demo board ready. Use Create server match to bind this UI to the authoritative backend.";
        }

        private void SetStatus(string message)
        {
            status = message;
        }

        private void EnqueueOnMainThread(Action action)
        {
            lock (mainThreadActionsLock)
            {
                mainThreadActions.Enqueue(action);
            }
        }

        private void DrainMainThreadActions()
        {
            while (true)
            {
                Action action;
                lock (mainThreadActionsLock)
                {
                    if (mainThreadActions.Count == 0)
                    {
                        return;
                    }

                    action = mainThreadActions.Dequeue();
                }

                action();
            }
        }

        private void EnsureStyles()
        {
            if (titleStyle != null)
            {
                return;
            }

            titleStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 58,
                fontStyle = FontStyle.Bold,
                normal = { textColor = Color.white }
            };
            bannerStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 32,
                fontStyle = FontStyle.Bold,
                normal = { textColor = new Color32(255, 224, 92, 255) }
            };
            hudStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 29,
                fontStyle = FontStyle.Bold,
                normal = { textColor = Color.white }
            };
            statusStyle = new GUIStyle(GUI.skin.label)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 23,
                wordWrap = true,
                normal = { textColor = new Color32(218, 228, 244, 255) }
            };
            cellStyle = new GUIStyle(GUI.skin.button)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 25,
                fontStyle = FontStyle.Bold,
                wordWrap = true
            };
            actionStyle = new GUIStyle(GUI.skin.button)
            {
                alignment = TextAnchor.MiddleCenter,
                fontSize = 27,
                fontStyle = FontStyle.Bold,
                wordWrap = true
            };
            inputStyle = new GUIStyle(GUI.skin.textField)
            {
                alignment = TextAnchor.MiddleLeft,
                fontSize = 24,
                wordWrap = false
            };
            overlayStyle = new GUIStyle(GUI.skin.box)
            {
                alignment = TextAnchor.UpperCenter,
                padding = new RectOffset(16, 16, 16, 16),
                normal = { background = MakeSolidTexture(new Color32(18, 26, 42, 242)), textColor = Color.white }
            };
        }

        private static Texture2D MakeSolidTexture(Color32 color)
        {
            var texture = new Texture2D(1, 1, TextureFormat.RGBA32, false);
            texture.SetPixel(0, 0, color);
            texture.Apply();
            return texture;
        }

        private sealed class CellViewModel
        {
            public CellViewModel(int cellId, string letter)
            {
                CellId = cellId;
                Letter = letter;
                OwnerSeat = -1;
            }

            public int CellId { get; private set; }
            public string Letter { get; private set; }
            public int OwnerSeat { get; set; }
            public bool Locked { get; set; }
            public float LockRemaining { get; set; }
        }


        private sealed class PendingIntentViewModel
        {
            public PendingIntentViewModel(uint sequence, int[] cellIds, string word, int seat, float submittedAt, uint submittedAtStateVersion)
            {
                Sequence = sequence;
                CellIds = cellIds == null ? new int[0] : cellIds;
                Word = word == null ? string.Empty : word;
                Seat = seat;
                SubmittedAt = submittedAt;
                SubmittedAtStateVersion = submittedAtStateVersion;
            }

            public uint Sequence { get; private set; }
            public int[] CellIds { get; private set; }
            public string Word { get; private set; }
            public int Seat { get; private set; }
            public float SubmittedAt { get; private set; }
            public uint SubmittedAtStateVersion { get; private set; }
            public bool Resolved { get; set; }
            public bool Accepted { get; set; }
            public bool Rejected { get; set; }
            public float ResolvedAt { get; set; }
            public uint StateVersion { get; set; }

            public bool ContainsCell(int cellId)
            {
                for (var index = 0; index < CellIds.Length; index++)
                {
                    if (CellIds[index] == cellId)
                    {
                        return true;
                    }
                }

                return false;
            }
        }

        private sealed class PlayerViewModel
        {
            public PlayerViewModel(string name)
            {
                Reset(name);
            }

            public string Name { get; private set; }
            public ulong UserId { get; set; }
            public int Score { get; set; }
            public float Combo { get; set; }
            public int Rank { get; set; }
            public bool Eliminated { get; set; }

            public string DisplayName
            {
                get
                {
                    return UserId == 0 ? Name : Name + " #" + UserId;
                }
            }

            public void Reset(string name)
            {
                Name = name;
                UserId = 0;
                Score = 0;
                Combo = 1f;
                Rank = 0;
                Eliminated = false;
            }
        }
    }
}
