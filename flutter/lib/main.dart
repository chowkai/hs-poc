import 'package:flutter/material.dart';
import 'hs_service.dart';

void main() => runApp(const HsPocApp());

class HsPocApp extends StatelessWidget {
  const HsPocApp({super.key});
  @override
  Widget build(BuildContext context) {
    return const MaterialApp(
      debugShowCheckedModeBanner: false,
      home: HomePage(),
    );
  }
}

class HomePage extends StatefulWidget {
  const HomePage({super.key});
  @override
  State<HomePage> createState() => _HomePageState();
}

class _HomePageState extends State<HomePage> {
  bool _connected = false;
  String _nodeIP = '';
  List<Map<String,String>> _peers = [];
  bool _loading = false;
  String _statusText = '';
  final HsService _hsService = HsService();
  final _serverCtrl = TextEditingController(text: 'http://10.0.2.2:9090');
  final _keyCtrl = TextEditingController(text: 'hs-poc-test-20260515');

  @override
  void dispose() { _serverCtrl.dispose(); _keyCtrl.dispose(); super.dispose(); }

  Color get _statusColor => _loading ? Colors.orange : (_connected ? Colors.green : Colors.red);
  String get _statusLabel => _statusText.isNotEmpty ? _statusText : (_loading ? 'Connecting...' : (_connected ? 'Connected' : 'Disconnected'));

  Future<void> _refresh() async {
    try {
      final status = await _hsService.status();
      if (mounted) {
        setState(() {
          _connected = status.connected;
          _nodeIP = status.ip;
          _peers = status.peers.map((p) => {'id': p.id, 'name': p.name, 'ip': p.ip}).toList();
        });
      }
    } catch (_) {}
  }

  Future<void> _connect() async {
    debugPrint('🔵 _connect() called');
    setState(() => _loading = true);
    try {
      debugPrint('🔵 calling register...');
      final nodeId = await _hsService.register(server: _serverCtrl.text, key: _keyCtrl.text);
      debugPrint('🟢 register done: $nodeId');
      debugPrint('🔵 calling connect...');
      final status = await _hsService.connect();
      debugPrint('🟢 connect result: $status');
      if (status == 'waiting_permission') {
        debugPrint('🟡 VPN permission required — user must accept dialog');
        if (mounted) setState(() { _loading = false; _statusText = 'VPN Permission Required'; });
        return;
      }
      if (mounted) setState(() { _loading = false; _connected = true; });
    } catch (e) {
      debugPrint('🔴 ERROR: $e');
      if (mounted) setState(() => _loading = false);
    }
  }

  Future<void> _disconnect() async {
    setState(() => _loading = true);
    await _hsService.disconnect();
    if (mounted) setState(() { _loading = false; _connected = false; _nodeIP = ''; _peers = []; });
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: const Color(0xFF1A1A2E),
      appBar: AppBar(
        title: const Text('HS-PoC Mesh', style: TextStyle(color: Colors.white)),
        backgroundColor: const Color(0xFF16213E),
        actions: [
          IconButton(icon: const Icon(Icons.refresh, color: Colors.white70), onPressed: _refresh),
        ],
      ),
      body: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            // Status Card
            Card(
              color: const Color(0xFF16213E),
              child: Padding(
                padding: const EdgeInsets.all(24),
                child: Row(
                  children: [
                    Icon(Icons.circle, color: _statusColor, size: 24),
                    const SizedBox(width: 16),
                    Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(_statusLabel, style: const TextStyle(color: Colors.white, fontSize: 20, fontWeight: FontWeight.bold)),
                        if (_nodeIP.isNotEmpty)
                          Text('IP: $_nodeIP', style: const TextStyle(color: Colors.grey)),
                      ],
                    ),
                    const Spacer(),
                    if (_loading)
                      const SizedBox(width: 24, height: 24, child: CircularProgressIndicator(strokeWidth: 2)),
                  ],
                ),
              ),
            ),
            const SizedBox(height: 12),
            // Buttons
            Row(
              children: [
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: _loading ? null : _connect,
                    icon: const Icon(Icons.power_settings_new),
                    label: const Text('Connect'),
                    style: ElevatedButton.styleFrom(backgroundColor: Colors.indigo, foregroundColor: Colors.white, padding: const EdgeInsets.symmetric(vertical: 14)),
                  ),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: OutlinedButton.icon(
                    onPressed: _loading ? null : _disconnect,
                    icon: const Icon(Icons.power_off),
                    label: const Text('Disconnect'),
                    style: OutlinedButton.styleFrom(foregroundColor: Colors.redAccent, padding: const EdgeInsets.symmetric(vertical: 14)),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 12),
            // Server Config
            Card(
              color: const Color(0xFF16213E),
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Text('Server Config', style: TextStyle(color: Colors.grey, fontSize: 12)),
                    const SizedBox(height: 6),
                    TextField(controller: _serverCtrl, decoration: const InputDecoration(labelText: 'Server URL', border: OutlineInputBorder(), isDense: true), style: const TextStyle(fontSize: 13, color: Colors.white)),
                    const SizedBox(height: 6),
                    TextField(controller: _keyCtrl, decoration: const InputDecoration(labelText: 'Admin Key', border: OutlineInputBorder(), isDense: true), style: const TextStyle(fontSize: 13, color: Colors.white), obscureText: true),
                  ],
                ),
              ),
            ),
            const SizedBox(height: 12),
            const Text('Peers', style: TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold)),
            const SizedBox(height: 6),
            Expanded(
              child: _peers.isEmpty
                  ? const Center(child: Text('No peers connected', style: TextStyle(color: Colors.grey)))
                  : ListView.builder(
                      itemCount: _peers.length,
                      itemBuilder: (ctx, i) {
                        final p = _peers[i];
                        return Card(
                          color: const Color(0xFF16213E),
                          child: ListTile(
                            leading: const Icon(Icons.computer, color: Colors.indigoAccent),
                            title: Text(p['name'] ?? '', style: const TextStyle(color: Colors.white)),
                            subtitle: Text('IP: ${p['ip'] ?? ''}', style: const TextStyle(color: Colors.grey)),
                          ),
                        );
                      },
                    ),
            ),
          ],
        ),
      ),
    );
  }
}