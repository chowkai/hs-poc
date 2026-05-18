import 'dart:io';
import 'dart:convert';
import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

/// Headscale client service — 纯 hs-client 二进制驱动，零外部依赖。
class HsService {
  static const String clientBin = 'hs-client';

  // 默认值（真机可用）
  String serverUrl = 'http://43.165.166.56:8080';
  String adminKey = 'hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY';

  File? _androidBinary;
  Process? _wgProcess;

  // ── Android 二进制提取 ────────────────────────────────────────

  /// 从 APK assets 提取 hs-client arm64 到可写目录，仅首次。
  Future<File> _ensureAndroidBinary() async {
    if (_androidBinary != null) return _androidBinary!;
    final binFile = File('/data/data/com.example.hs_poc/files/hs-client');
    if (!await binFile.exists()) {
      final data = await rootBundle.load('assets/hs-client-android');
      await binFile.writeAsBytes(data.buffer.asUint8List());
      await Process.run('chmod', ['755', binFile.path]);
    }
    _androidBinary = binFile;
    return binFile;
  }

  /// Android 的数据目录（HOME），hs-client 在这里存状态。
  String get _androidHome => '/data/data/com.example.hs_poc/files';

  // ── 平台路径 ──────────────────────────────────────────────────

  String get clientPath {
    if (Platform.isWindows) return 'assets/hs-client.exe';
    if (Platform.isLinux) return 'assets/hs-client-linux';
    if (Platform.isAndroid) return 'assets/hs-client-android';
    return 'assets/hs-client-linux';
  }

  // ── 注册 ──────────────────────────────────────────────────────

  Future<String> register({required String server, required String key, String? name}) async {
    serverUrl = server;
    adminKey = key;

    if (Platform.isAndroid) {
      final bin = await _ensureAndroidBinary();
      final args = ['register', '--server', server, '--key', key];
      if (name != null) args.addAll(['--name', name]);

      final result = await Process.run(bin.path, args, environment: {'HOME': _androidHome});
      if (result.exitCode != 0) {
        throw Exception('hs-client register failed: ${result.stderr}');
      }
      return (result.stdout as String).trim();
    }

    // 桌面端
    final args = ['register', '--server', server, '--key', key];
    if (name != null) args.addAll(['--name', name]);
    return (await _run(args)).trim();
  }

  // ── 连接 (WG up) ─────────────────────────────────────────────

  Future<String> connect() async {
    if (Platform.isAndroid) {
      final bin = await _ensureAndroidBinary();
      final args = ['up', '--server', serverUrl, '--key', adminKey];

      _wgProcess = await Process.start(bin.path, args, environment: {'HOME': _androidHome});
      _wgProcess!.stdout.transform(utf8.decoder).listen((d) => debugPrint('hs-client: $d'));
      _wgProcess!.stderr.transform(utf8.decoder).listen((d) => debugPrint('hs-client err: $d'));
      return 'connecting';
    }

    // 桌面端
    final args = ['up', '--server', serverUrl, '--key', adminKey];
    _wgProcess = await Process.start(clientPath, args);
    _wgProcess!.stdout.transform(utf8.decoder).listen((d) => debugPrint('hs-client: $d'));
    _wgProcess!.stderr.transform(utf8.decoder).listen((d) => debugPrint('hs-client err: $d'));
    return 'connecting';
  }

  // ── 断开 ──────────────────────────────────────────────────────

  Future<void> disconnect() async {
    if (Platform.isAndroid) {
      try {
        final bin = await _ensureAndroidBinary();
        await Process.run(bin.path, ['down'], environment: {'HOME': _androidHome});
      } catch (_) {}
      _wgProcess?.kill();
      _wgProcess = null;
      return;
    }

    try {
      await _run(['down']);
    } catch (_) {}
    _wgProcess?.kill();
    _wgProcess = null;
  }

  // ── 状态 ──────────────────────────────────────────────────────

  Future<HsStatus> status() async {
    if (Platform.isAndroid) {
      try {
        final bin = await _ensureAndroidBinary();
        final result = await Process.run(bin.path, ['status'], environment: {'HOME': _androidHome});
        if (result.exitCode == 0) {
          final json = jsonDecode(result.stdout as String) as Map<String, dynamic>;
          return HsStatus.fromJson(json);
        }
      } catch (_) {}
      return HsStatus(connected: false, ip: '', peers: []);
    }

    try {
      final output = await _run(['status']);
      final json = jsonDecode(output) as Map<String, dynamic>;
      return HsStatus.fromJson(json);
    } catch (_) {
      return HsStatus(connected: false, ip: '', peers: []);
    }
  }

  // ── Ping ──────────────────────────────────────────────────────

  Future<String> ping(String targetIP) async {
    if (Platform.isAndroid) {
      try {
        final bin = await _ensureAndroidBinary();
        final result = await Process.run(bin.path, ['ping', targetIP], environment: {'HOME': _androidHome});
        return (result.stdout as String).trim();
      } catch (e) {
        return 'Ping failed: $e';
      }
    }

    try {
      final result = await Process.run(clientPath, ['ping', targetIP]);
      return (result.stdout as String).trim();
    } catch (e) {
      return 'Ping failed: $e';
    }
  }

  // ── 内部方法 ──────────────────────────────────────────────────

  Future<String> _run(List<String> args) async {
    final result = await Process.run(clientPath, args);
    if (result.exitCode != 0) {
      throw Exception('hs-client failed: ${result.stderr}');
    }
    return (result.stdout as String).trim();
  }
}

// ── 数据模型 ────────────────────────────────────────────────────

class HsStatus {
  final bool connected;
  final String ip;
  final List<HsPeer> peers;

  HsStatus({required this.connected, required this.ip, required this.peers});

  factory HsStatus.fromJson(Map<String, dynamic> json) {
    return HsStatus(
      connected: json['connected'] ?? false,
      ip: json['ip'] ?? '',
      peers: (json['peers'] as List<dynamic>?)
              ?.map((p) => HsPeer.fromJson(p as Map<String, dynamic>))
              .toList() ??
          [],
    );
  }
}

class HsPeer {
  final String id;
  final String name;
  final String ip;

  HsPeer({required this.id, required this.name, required this.ip});

  factory HsPeer.fromJson(Map<String, dynamic> json) {
    return HsPeer(
      id: json['id'] ?? '',
      name: json['name'] ?? '',
      ip: json['ip'] ?? '',
    );
  }
}