package com.example.hs_poc

import android.app.admin.DevicePolicyManager
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.VpnService
import android.util.Log
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {

    companion object {
        const val TAG = "MainActivity"
        const val CHANNEL = "com.hspoc/vpn"
        const val VPN_REQUEST = 42
    }

    private var vpnStatusReceiver: BroadcastReceiver? = null
    private var pendingVpnIntent: Intent? = null

    override fun onCreate(savedInstanceState: android.os.Bundle?) {
        super.onCreate(savedInstanceState)
        ensureVpnPermissionForDebug()
    }

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)

        val channel = MethodChannel(flutterEngine.dartExecutor.binaryMessenger, CHANNEL)

        channel.setMethodCallHandler { call, result ->
            when (call.method) {
                "startVpn" -> {
                    val privateKey = call.argument<String>("privateKey") ?: ""
                    val peerKey = call.argument<String>("peerKey") ?: ""
                    val endpoint = call.argument<String>("endpoint") ?: ""
                    val localIP = call.argument<String>("localIP") ?: "100.64.0.2"
                    val peerIP = call.argument<String>("peerIP") ?: "100.64.0.1"

                    val intent = Intent(this, HsVpnService::class.java).apply {
                        action = HsVpnService.ACTION_CONNECT
                        putExtra("privateKey", privateKey)
                        putExtra("peerKey", peerKey)
                        putExtra("endpoint", endpoint)
                        putExtra("localIP", localIP)
                        putExtra("peerIP", peerIP)
                    }

                    val prepIntent = VpnService.prepare(this)
                    if (prepIntent != null) {
                        pendingVpnIntent = intent
                        startActivityForResult(prepIntent, VPN_REQUEST)
                        result.success("waiting_permission")
                        return@setMethodCallHandler
                    }

                    startService(intent)
                    result.success("connecting")
                }
                "stopVpn" -> {
                    val intent = Intent(this, HsVpnService::class.java).apply {
                        action = HsVpnService.ACTION_DISCONNECT
                    }
                    startService(intent)
                    result.success("disconnecting")
                }
                "status" -> {
                    result.success(if (HsVpnService.isRunning) "connected" else "disconnected")
                }
                else -> {
                    result.notImplemented()
                }
            }
        }

        vpnStatusReceiver = object : BroadcastReceiver() {
            override fun onReceive(context: Context?, intent: Intent?) {
                val connected = intent?.getBooleanExtra(HsVpnService.EXTRA_CONNECTED, false) ?: false
                channel.invokeMethod("vpnStatusChanged", connected)
            }
        }
        registerReceiver(
            vpnStatusReceiver,
            IntentFilter(HsVpnService.BROADCAST_STATUS),
            Context.RECEIVER_NOT_EXPORTED
        )
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == VPN_REQUEST && resultCode == RESULT_OK) {
            pendingVpnIntent?.let { startService(it) }
            pendingVpnIntent = null
        }
    }

    override fun onDestroy() {
        vpnStatusReceiver?.let { unregisterReceiver(it) }
        super.onDestroy()
    }

    /**
     * Debug mode: use Device Admin + setAlwaysOnVpnPackage to skip VPN dialog.
     * Mirrors Kinnect's ensureVpnPermissionForDebug().
     */
    private fun ensureVpnPermissionForDebug() {
        try {
            val dpm = getSystemService(Context.DEVICE_POLICY_SERVICE) as DevicePolicyManager
            val adminComponent = ComponentName(this, VpnDeviceAdminReceiver::class.java)

            if (dpm.isAdminActive(adminComponent)) {
                Log.d(TAG, "Device admin active, setting always-on VPN")
                dpm.setAlwaysOnVpnPackage(adminComponent, packageName, true)
                Log.d(TAG, "Always-on VPN set for $packageName")
            } else {
                Log.w(TAG, "Device admin NOT active — run: adb shell dpm set-device-owner $packageName/.VpnDeviceAdminReceiver")
            }
        } catch (e: Exception) {
            Log.e(TAG, "ensureVpnPermissionForDebug failed: ${e.message}")
        }
    }
}