using System;
using System.Collections.Generic;
using UnityEngine;
using UnityEngine.EventSystems;
using UnityEngine.UI;

namespace Words.Client
{
    /// <summary>
    /// M1 client UX bootstrap. This is a non-authoritative presentation/demo
    /// harness: it visualizes the shared board, claim/lock/cross-steal states,
    /// combo and Sudden Death banners while the server remains the only source
    /// of competitive truth.
    ///
    /// The object is created at runtime so the minimal scene can keep building
    /// on CI without requiring a Linux/ARM64 Unity Editor session to serialize
    /// GameObjects.
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

        private Font font;
        private Text statusText;
        private Text currentWordText;
        private Text scoreText;
        private Text comboText;
        private Text suddenDeathText;
        private int activeSeat;
        private bool suddenDeath;
        private float lastAcceptedAt = -999f;

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.AfterSceneLoad)]
        private static void Bootstrap()
        {
            if (FindObjectOfType<WordArenaBootstrap>() != null)
            {
                return;
            }

            var host = new GameObject("WordArenaBootstrap");
            DontDestroyOnLoad(host);
            host.AddComponent<WordArenaBootstrap>();
        }

        private void Awake()
        {
            font = LoadBuiltInFont();
            BuildUI();
            SetStatus("Demo board ready. Server-authoritative networking is the next client slice.");
            RefreshAllCells();
            RefreshHud();
        }

        private void Update()
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
                else
                {
                    UpdateCellVisual(cell);
                }
            }

            if (changed)
            {
                RefreshAllCells();
                SetStatus("Locks expired: owned cells can now be cross-stolen in the demo.");
            }
        }

        private void BuildUI()
        {
            EnsureEventSystem();

            var canvasObject = new GameObject("WordArenaCanvas", typeof(Canvas), typeof(CanvasScaler), typeof(GraphicRaycaster));
            canvasObject.transform.SetParent(transform, false);
            var canvas = canvasObject.GetComponent<Canvas>();
            canvas.renderMode = RenderMode.ScreenSpaceOverlay;

            var scaler = canvasObject.GetComponent<CanvasScaler>();
            scaler.uiScaleMode = CanvasScaler.ScaleMode.ScaleWithScreenSize;
            scaler.referenceResolution = new Vector2(1080f, 1920f);
            scaler.matchWidthOrHeight = 0.5f;

            var root = CreatePanel("Root", canvasObject.transform, new Color32(16, 22, 34, 255));
            Stretch(root.rectTransform, Vector2.zero, Vector2.one, Vector2.zero, Vector2.zero);

            var title = CreateText("Title", root.transform, "Word Arena", 58, FontStyle.Bold, TextAnchor.MiddleCenter, Color.white);
            Stretch(title.rectTransform, new Vector2(0.06f, 0.90f), new Vector2(0.94f, 0.98f), Vector2.zero, Vector2.zero);

            suddenDeathText = CreateText("SuddenDeathBanner", root.transform, "", 34, FontStyle.Bold, TextAnchor.MiddleCenter, new Color32(255, 224, 92, 255));
            Stretch(suddenDeathText.rectTransform, new Vector2(0.06f, 0.84f), new Vector2(0.94f, 0.90f), Vector2.zero, Vector2.zero);

            scoreText = CreateText("ScoreText", root.transform, "", 34, FontStyle.Bold, TextAnchor.MiddleCenter, Color.white);
            Stretch(scoreText.rectTransform, new Vector2(0.06f, 0.78f), new Vector2(0.94f, 0.84f), Vector2.zero, Vector2.zero);

            comboText = CreateText("ComboText", root.transform, "", 28, FontStyle.Normal, TextAnchor.MiddleCenter, new Color32(180, 212, 255, 255));
            Stretch(comboText.rectTransform, new Vector2(0.06f, 0.73f), new Vector2(0.94f, 0.78f), Vector2.zero, Vector2.zero);

            var board = CreatePanel("SharedBoard", root.transform, new Color32(25, 34, 51, 255));
            Stretch(board.rectTransform, new Vector2(0.08f, 0.31f), new Vector2(0.92f, 0.71f), Vector2.zero, Vector2.zero);
            var grid = board.gameObject.AddComponent<GridLayoutGroup>();
            grid.constraint = GridLayoutGroup.Constraint.FixedColumnCount;
            grid.constraintCount = 4;
            grid.spacing = new Vector2(16f, 16f);
            grid.padding = new RectOffset(18, 18, 18, 18);
            grid.cellSize = new Vector2(205f, 150f);

            for (var index = 0; index < DemoLetters.Length; index++)
            {
                var cell = new CellViewModel(index, DemoLetters[index]);
                cells.Add(cell);
                BuildCell(board.transform, cell);
            }

            currentWordText = CreateText("CurrentWord", root.transform, "", 36, FontStyle.Bold, TextAnchor.MiddleCenter, Color.white);
            Stretch(currentWordText.rectTransform, new Vector2(0.06f, 0.24f), new Vector2(0.94f, 0.30f), Vector2.zero, Vector2.zero);

            statusText = CreateText("Status", root.transform, "", 26, FontStyle.Normal, TextAnchor.MiddleCenter, new Color32(218, 228, 244, 255));
            Stretch(statusText.rectTransform, new Vector2(0.06f, 0.16f), new Vector2(0.94f, 0.24f), Vector2.zero, Vector2.zero);

            var actions = CreatePanel("Actions", root.transform, new Color32(0, 0, 0, 0));
            Stretch(actions.rectTransform, new Vector2(0.06f, 0.04f), new Vector2(0.94f, 0.15f), Vector2.zero, Vector2.zero);
            var actionGrid = actions.gameObject.AddComponent<GridLayoutGroup>();
            actionGrid.constraint = GridLayoutGroup.Constraint.FixedColumnCount;
            actionGrid.constraintCount = 2;
            actionGrid.spacing = new Vector2(18f, 14f);
            actionGrid.cellSize = new Vector2(450f, 76f);

            CreateButton(actions.transform, "Claim selected", OnClaimSelected, new Color32(63, 137, 255, 255));
            CreateButton(actions.transform, "Switch seat", OnSwitchSeat, new Color32(255, 142, 63, 255));
            CreateButton(actions.transform, "Clear path", OnClearSelection, new Color32(90, 105, 128, 255));
            CreateButton(actions.transform, "Sudden Death", OnToggleSuddenDeath, new Color32(155, 84, 255, 255));
        }

        private void BuildCell(Transform parent, CellViewModel cell)
        {
            var button = CreateButton(parent, cell.Letter, () => OnCellTapped(cell.Index), new Color32(54, 67, 91, 255));
            button.gameObject.name = $"Cell_{cell.Index}_{cell.Letter}";
            cell.Button = button;
            cell.Label = button.GetComponentInChildren<Text>();
        }

        private void OnCellTapped(int index)
        {
            if (selected.Contains(index))
            {
                selected.Remove(index);
            }
            else
            {
                selected.Add(index);
            }

            RefreshAllCells();
            RefreshHud();
        }

        private void OnClaimSelected()
        {
            var word = CurrentWord();
            if (word.Length < 3)
            {
                SetStatus("Select at least three cells. The server will validate the real word path.");
                return;
            }

            if (!DemoWords.Contains(word))
            {
                SetStatus($"'{word}' rejected in local UX demo. Production validity is server-side only.");
                selected.Clear();
                RefreshAllCells();
                RefreshHud();
                return;
            }

            var stole = false;
            foreach (var index in selected)
            {
                var cell = cells[index];
                if (cell.Locked && cell.OwnerSeat != activeSeat)
                {
                    SetStatus($"Cell {index} is locked for {cell.LockRemaining:0.0}s; wait before a Cross-Steal.");
                    return;
                }
            }

            foreach (var index in selected)
            {
                var cell = cells[index];
                if (cell.OwnerSeat >= 0 && cell.OwnerSeat != activeSeat)
                {
                    stole = true;
                }

                cell.OwnerSeat = activeSeat;
                cell.Locked = true;
                cell.LockRemaining = LockSeconds;
            }

            ApplyScore(word, stole);
            selected.Clear();
            RefreshAllCells();
            RefreshHud();
            SetStatus(stole
                ? $"{players[activeSeat].Name} cross-stole '{word}'. Server event will be authoritative."
                : $"{players[activeSeat].Name} claimed '{word}' and locked the cells for 3s.");
        }

        private void OnSwitchSeat()
        {
            activeSeat = 1 - activeSeat;
            selected.Clear();
            RefreshAllCells();
            RefreshHud();
            SetStatus($"Active seat: {players[activeSeat].Name}. Shared board remains identical for both players.");
        }

        private void OnClearSelection()
        {
            selected.Clear();
            RefreshAllCells();
            RefreshHud();
            SetStatus("Selection cleared.");
        }

        private void OnToggleSuddenDeath()
        {
            suddenDeath = !suddenDeath;
            RefreshHud();
            SetStatus(suddenDeath
                ? "Sudden Death presentation on: first accepted server word wins the tiebreak."
                : "Sudden Death presentation off: tied matches render as draws.");
        }

        private void ApplyScore(string word, bool stole)
        {
            var now = Time.time;
            var player = players[activeSeat];
            player.Combo = now - lastAcceptedAt <= ComboWindowSeconds ? player.Combo + 1 : 1;
            lastAcceptedAt = now;

            var comboBonus = Math.Max(0, player.Combo - 1);
            var stealBonus = stole ? 2 : 0;
            var suddenBonus = suddenDeath ? 1 : 0;
            player.Score += word.Length + comboBonus + stealBonus + suddenBonus;
        }

        private string CurrentWord()
        {
            var result = string.Empty;
            foreach (var index in selected)
            {
                result += cells[index].Letter;
            }

            return result;
        }

        private void RefreshAllCells()
        {
            foreach (var cell in cells)
            {
                UpdateCellVisual(cell);
            }
        }

        private void UpdateCellVisual(CellViewModel cell)
        {
            var image = cell.Button.targetGraphic as Image;
            if (image != null)
            {
                image.color = CellColor(cell);
            }

            if (cell.Label != null)
            {
                var owner = cell.OwnerSeat < 0 ? "Free" : players[cell.OwnerSeat].Name;
                var lockText = cell.Locked ? $"\nLOCK {cell.LockRemaining:0.0}s" : string.Empty;
                var selectedText = selected.Contains(cell.Index) ? "\nSELECTED" : string.Empty;
                cell.Label.text = $"{cell.Letter}\n{owner}{lockText}{selectedText}";
            }
        }

        private Color32 CellColor(CellViewModel cell)
        {
            if (selected.Contains(cell.Index))
            {
                return new Color32(248, 210, 88, 255);
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

        private void RefreshHud()
        {
            currentWordText.text = selected.Count == 0 ? "Tap cells to spell CAT or DOG" : $"Selected: {CurrentWord()}";
            scoreText.text = $"Blue {players[0].Score}  —  {players[1].Score} Orange   | active: {players[activeSeat].Name}";
            comboText.text = $"Combo: Blue x{Math.Max(1, players[0].Combo)} / Orange x{Math.Max(1, players[1].Combo)}";
            suddenDeathText.text = suddenDeath ? "SUDDEN DEATH — first accepted word wins" : "Shared board UX prototype";
        }

        private void SetStatus(string message)
        {
            if (statusText != null)
            {
                statusText.text = message;
            }
        }

        private Text CreateText(string name, Transform parent, string text, int size, FontStyle style, TextAnchor anchor, Color color)
        {
            var go = new GameObject(name, typeof(RectTransform), typeof(Text));
            go.transform.SetParent(parent, false);
            var label = go.GetComponent<Text>();
            label.text = text;
            label.font = font;
            label.fontSize = size;
            label.fontStyle = style;
            label.alignment = anchor;
            label.color = color;
            label.horizontalOverflow = HorizontalWrapMode.Wrap;
            label.verticalOverflow = VerticalWrapMode.Overflow;
            return label;
        }

        private Image CreatePanel(string name, Transform parent, Color32 color)
        {
            var go = new GameObject(name, typeof(RectTransform), typeof(Image));
            go.transform.SetParent(parent, false);
            var image = go.GetComponent<Image>();
            image.color = color;
            return image;
        }

        private Button CreateButton(Transform parent, string label, UnityEngine.Events.UnityAction action, Color32 color)
        {
            var go = new GameObject(label, typeof(RectTransform), typeof(Image), typeof(Button));
            go.transform.SetParent(parent, false);
            var image = go.GetComponent<Image>();
            image.color = color;

            var button = go.GetComponent<Button>();
            button.targetGraphic = image;
            button.onClick.AddListener(action);

            var text = CreateText("Label", go.transform, label, 28, FontStyle.Bold, TextAnchor.MiddleCenter, Color.white);
            Stretch(text.rectTransform, Vector2.zero, Vector2.one, Vector2.zero, Vector2.zero);
            return button;
        }

        private static void Stretch(RectTransform rectTransform, Vector2 anchorMin, Vector2 anchorMax, Vector2 offsetMin, Vector2 offsetMax)
        {
            rectTransform.anchorMin = anchorMin;
            rectTransform.anchorMax = anchorMax;
            rectTransform.offsetMin = offsetMin;
            rectTransform.offsetMax = offsetMax;
        }

        private static Font LoadBuiltInFont()
        {
            var loaded = Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            if (loaded == null)
            {
                loaded = Resources.GetBuiltinResource<Font>("Arial.ttf");
            }

            return loaded;
        }

        private static void EnsureEventSystem()
        {
            if (FindObjectOfType<EventSystem>() != null)
            {
                return;
            }

            var eventSystem = new GameObject("EventSystem", typeof(EventSystem), typeof(StandaloneInputModule));
            DontDestroyOnLoad(eventSystem);
        }

        private sealed class CellViewModel
        {
            public CellViewModel(int index, string letter)
            {
                Index = index;
                Letter = letter;
                OwnerSeat = -1;
            }

            public int Index { get; }
            public string Letter { get; }
            public int OwnerSeat { get; set; }
            public bool Locked { get; set; }
            public float LockRemaining { get; set; }
            public Button Button { get; set; }
            public Text Label { get; set; }
        }

        private sealed class PlayerViewModel
        {
            public PlayerViewModel(string name)
            {
                Name = name;
            }

            public string Name { get; }
            public int Score { get; set; }
            public int Combo { get; set; }
        }
    }
}
