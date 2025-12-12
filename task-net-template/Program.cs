var assembly = Assembly.GetExecutingAssembly();
var resourceName = "MyGoTool.go-tool.exe";

using var stream = assembly.GetManifestResourceStream(resourceName);
var tempPath = Path.GetTempFileName() + ".exe";
using var fileStream = File.Create(tempPath);
stream.CopyTo(fileStream);

// Execute the extracted executable
var process = Process.Start(tempPath, string.Join(" ", args));
process.WaitForExit();
return process.ExitCode;
