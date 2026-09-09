using System;
using System.IO;
using UnityEditor;
using UnityEditor.Build;
using UnityEditor.Build.Reporting;
using UnityEngine;

namespace Words
{
    /// <summary>
    /// Deterministic, non-interactive Android entry point for CI and local Editor use.
    /// </summary>
    public static class BuildCommand
    {
        private const string ScenePath = "Assets/Scenes/Main.unity";
        private const string DefaultBuildPath = "Builds/Android/words-debug.apk";

        public static void BuildAndroid()
        {
            try
            {
                var buildType = GetArgument("-buildType", Environment.GetEnvironmentVariable("WORDS_BUILD_TYPE") ?? "debug").ToLowerInvariant();
                var buildPath = GetArgument("-buildPath", Environment.GetEnvironmentVariable("WORDS_BUILD_PATH") ?? DefaultBuildPath);

                if (buildType != "debug" && buildType != "release")
                {
                    throw new ArgumentException("-buildType must be either 'debug' or 'release'.");
                }

                if (!File.Exists(Path.Combine(Directory.GetCurrentDirectory(), ScenePath)))
                {
                    throw new FileNotFoundException("Required build scene was not found.", ScenePath);
                }

                EditorUserBuildSettings.SwitchActiveBuildTarget(BuildTargetGroup.Android, BuildTarget.Android);
                EditorBuildSettings.scenes = new[]
                {
                    new EditorBuildSettingsScene(ScenePath, true)
                };

                PlayerSettings.companyName = "JoTalbot";
                PlayerSettings.productName = "Word Arena";
                PlayerSettings.applicationIdentifier = "com.jotalbot.words";
                PlayerSettings.bundleVersion = "0.1.0";
                PlayerSettings.Android.bundleVersionCode = 1;
                PlayerSettings.SetScriptingBackend(NamedBuildTarget.Android, ScriptingImplementation.IL2CPP);
                PlayerSettings.Android.targetArchitectures = buildType == "debug"
                    ? AndroidArchitecture.ARM64 | AndroidArchitecture.X86_64
                    : AndroidArchitecture.ARM64;
                EditorUserBuildSettings.buildAppBundle = false;

                var outputPath = Path.GetFullPath(buildPath);
                var outputDirectory = Path.GetDirectoryName(outputPath);
                if (!string.IsNullOrEmpty(outputDirectory))
                {
                    Directory.CreateDirectory(outputDirectory);
                }

                var options = BuildOptions.StrictMode | BuildOptions.CompressWithLz4HC;
                if (buildType == "debug")
                {
                    options |= BuildOptions.Development | BuildOptions.AllowDebugging;
                }

                var report = BuildPipeline.BuildPlayer(new BuildPlayerOptions
                {
                    scenes = new[] { ScenePath },
                    locationPathName = outputPath,
                    target = BuildTarget.Android,
                    targetGroup = BuildTargetGroup.Android,
                    options = options
                });

                if (report.summary.result != BuildResult.Succeeded)
                {
                    throw new BuildFailedException(
                        $"Android build failed: {report.summary.result}; errors={report.summary.totalErrors}; warnings={report.summary.totalWarnings}");
                }

                Debug.Log($"Word Arena Android build succeeded: {outputPath} ({report.summary.totalSize} bytes)");
                EditorApplication.Exit(0);
            }
            catch (Exception exception)
            {
                Debug.LogException(exception);
                EditorApplication.Exit(1);
            }
        }

        private static string GetArgument(string name, string fallback)
        {
            var args = Environment.GetCommandLineArgs();
            for (var index = 0; index < args.Length - 1; index++)
            {
                if (string.Equals(args[index], name, StringComparison.OrdinalIgnoreCase))
                {
                    return args[index + 1];
                }
            }

            return fallback;
        }
    }
}
