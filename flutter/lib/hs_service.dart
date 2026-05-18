import 'dart:io';
import 'dart:convert';
import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

/// Headscale client service that manages the hs-client binary lifecycle.
class HsService {
  static const String clientBin = 'hs-client';
  static const _vpnChannel = MethodChannel('com.hspoc/vpn');

  String serverUrl = 'http://127.0.0.1:18080';
  String adminKey = 'poc-admin-key-change-me';

  String get clientPath {
    if (Platform.isWindows) return 'assets/hs-client.exe';
    if (Platform.isLinux) return 'assets/hs-client-linux';
    if (Platform.isAndroid) return 'assets/hs-client-android';
    return 'assets/hs-client-linux';
  }

  Future<String> _run(List<String> args) async {
    final result = await Process.run(clientPath, args);
    if (result.exitCode != 0) {
      throw Exception('hs-client failed: ${result.stderr}');
    }
    return (result.stdout as String).trim();
  }

  Future<String> register({required String server, required String key, String? name}) async {
    if (Platform.isAndroid) {
      final httpClient = HttpClient();
      try {
        final uri = Uri.parse('$server/api/v1/node/register');
        final request = await httpClient.postUrl(uri);
        request.headers.set('Content-Type', 'application/json');
        request.headers.set('Authorization', 'Bearer $key');
        request.write(jsonEncode({'name': name ?? 'android-device'}));
        final response = await request.close();
        if (response.statusCode == 200) {
          final body = await response.transform(utf8.decoder).join();
          final data = jsonDecode(body);
          return data['id'] ?? data['node_id'] ?? 'registered';
        }
        throw Exception('Registration failed: ${response.statusCode}');
      } finally {
        httpClient.close();
      }
    }

    final args = ['register', '--server', server, '--key', key];
    if (name != null) args.addAll(['--name', name]);
    final nodeId = await _run(args);
    return nodeId;
  }

  Future<String> connect({
    String? server,
    String? key,
    String? nodeId,
    String? privateKey,
    String? peerKey,
    String? endpoint,
    String? localIP,
    String? peerIP,
  }) async {
    if (Platform.isAndroid) {
      _vpnChannel.setMethodCallHandler((call) async {
        // handle vpnStatusChanged invocations from native
      });
      final result = await _vpnChannel.invokeMethod<String>('startVpn', {
        'privateKey': 'cIZUuc+tUujfZmeay6cRdQ++Ab7OLzKTWWLnQa+ZcFk=',
        'peerKey': 'Di+wh0LxYyf/2KLT1wdVTDciF7mzN0qE1ZkAU904H1Y=',
        'endpoint': '10.0.2.2:46378',
        'localIP': '100.64.0.2',
        'peerIP': '100.64.0.1',
      });
      return result ?? 'connecting';
    }

    final args = ['up'];
    if (server != null) args.addAll(['--server', server]);
    if (key != null) args.addAll(['--key', key]);
    if (nodeId != null) args.addAll(['--id', nodeId]);

    final process = await Process.start(clientPath, args);
    process.stdout.listen((data) {
      debugPrint('hs-client: ${utf8.decode(data)}');
    });
    process.stderr.listen((data) {
      debugPrint('hs-client err: ${utf8.decode(data)}');
    });
    return 'connecting';
  }

  Future<void> disconnect() async {
    if (Platform.isAndroid) {
      await _vpnChannel.invokeMethod('stopVpn');
      return;
    }
    await _run(['down']);
  }

  Future<HsStatus> status() async {
    if (Platform.isAndroid) {
      try {
        final result = await _vpnChannel.invokeMethod<String>('status');
        final connected = result == 'connected';
        return HsStatus(connected: connected, ip: '', peers: []);
      } catch (e) {
        return HsStatus(connected: false, ip: '', peers: []);
      }
    }

    try {
      final output = await _run(['status']);
      final json = jsonDecode(output) as Map<String, dynamic>;
      return HsStatus.fromJson(json);
    } catch (e) {
      return HsStatus(connected: false, ip: '', peers: []);
    }
  }

  Future<String> ping(String targetIP) async {
    if (Platform.isAndroid) {
      try {
        final result = await Process.run('ping', ['-c', '3', targetIP]);
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
}

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