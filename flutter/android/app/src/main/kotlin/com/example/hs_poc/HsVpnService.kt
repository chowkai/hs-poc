package com.example.hs_poc

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.util.Log
import com.wireguard.android.backend.GoBackend
import com.wireguard.android.backend.Tunnel
import com.wireguard.config.Config
import kotlin.concurrent.thread

class HsVpnService : Service() {

    companion object {
        const val TAG = "HsVpnService"
        const val ACTION_CONNECT = "com.example.hs_poc.VPN_CONNECT"
        const val ACTION_DISCONNECT = "com.example.hs_poc.VPN_DISCONNECT"
        const val BROADCAST_STATUS = "com.example.hs_poc.VPN_STATUS"
        const val EXTRA_CONNECTED = "connected"
        const val NOTIFICATION_CHANNEL_ID = "hs_vpn_channel"
        const val NOTIFICATION_ID = 1
        const val NOTIFICATION_CHANNEL_NAME = "HS VPN"
        const val TUNNEL_NAME = "hs-poc"

        @Volatile
        var isRunning = false
            private set
    }

    private val mainHandler = Handler(Looper.getMainLooper())
    private var backend: GoBackend? = null
    private var currentTunnel: Tunnel? = null

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent == null) return START_NOT_STICKY

        when (intent.action) {
            ACTION_CONNECT -> {
                val privateKey = intent.getStringExtra("privateKey") ?: ""
                val peerKey = intent.getStringExtra("peerKey") ?: ""
                val endpoint = intent.getStringExtra("endpoint") ?: ""
                val localIP = intent.getStringExtra("localIP") ?: "100.64.0.2"

                connect(privateKey, peerKey, endpoint, localIP)
            }
            ACTION_DISCONNECT -> disconnect()
        }

        return START_STICKY
    }

    private fun connect(
        privateKey: String,
        peerKey: String,
        endpoint: String,
        localIP: String
    ) {
        if (isRunning) {
            Log.w(TAG, "VPN already running")
            broadcastStatus(true)
            return
        }

        if (privateKey.isBlank() || peerKey.isBlank()) {
            Log.e(TAG, "Key or endpoint missing")
            broadcastStatus(false)
            return
        }

        val wgConf = buildWgQuickConfig(privateKey, peerKey, endpoint, localIP)

        val config: Config
        try {
            config = Config.parse(wgConf.byteInputStream())
        } catch (e: Exception) {
            Log.e(TAG, "Failed to parse WireGuard config", e)
            broadcastStatus(false)
            return
        }

        val backend = GoBackend(this)
        this.backend = backend

        val tunnel = object : Tunnel {
            override fun getName() = TUNNEL_NAME

            override fun onStateChange(newState: Tunnel.State) {
                Log.i(TAG, "Tunnel state: $newState")

                mainHandler.post {
                    if (newState == Tunnel.State.UP) {
                        isRunning = true
                        startForeground(NOTIFICATION_ID, buildNotification("Connected to HS VPN"))
                        broadcastStatus(true)
                        Log.i(TAG, "VPN connected: local=$localIP, endpoint=$endpoint")
                    } else {
                        cleanup()
                        broadcastStatus(false)
                    }
                }
            }
        }
        currentTunnel = tunnel

        thread(name = "WgGoBackend") {
            try {
                backend.setState(tunnel, Tunnel.State.UP, config)
            } catch (e: Exception) {
                Log.e(TAG, "GoBackend setState failed", e)
                mainHandler.post {
                    cleanup()
                    broadcastStatus(false)
                }
            }
        }
    }

    private fun buildWgQuickConfig(
        privateKey: String,
        peerKey: String,
        endpoint: String,
        localIP: String
    ) = buildString {
        appendLine("[Interface]")
        appendLine("PrivateKey = $privateKey")
        appendLine("Address = $localIP/32")
        appendLine("DNS = 1.1.1.1")
        appendLine()
        appendLine("[Peer]")
        appendLine("PublicKey = $peerKey")
        appendLine("Endpoint = $endpoint")
        appendLine("AllowedIPs = 0.0.0.0/0")
        appendLine("PersistentKeepalive = 25")
    }

    private fun cleanup() {
        isRunning = false
        currentTunnel = null
        backend = null
        stopForeground(STOP_FOREGROUND_REMOVE)
    }

    private fun disconnect() {
        val b = backend ?: return
        val t = currentTunnel ?: return

        thread(name = "WgGoBackend-Down") {
            try {
                b.setState(t, Tunnel.State.DOWN, null)
            } catch (e: Exception) {
                Log.e(TAG, "GoBackend setState DOWN failed", e)
            }
            mainHandler.post {
                cleanup()
                broadcastStatus(false)
                Log.i(TAG, "VPN disconnected")
            }
        }
    }

    override fun onDestroy() {
        disconnect()
        super.onDestroy()
    }

    private fun broadcastStatus(connected: Boolean) {
        val intent = Intent(BROADCAST_STATUS).apply {
            putExtra(EXTRA_CONNECTED, connected)
            setPackage(packageName)
        }
        sendBroadcast(intent)
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                NOTIFICATION_CHANNEL_ID,
                NOTIFICATION_CHANNEL_NAME,
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = "Headscale VPN Connection Status"
            }
            val manager = getSystemService(NotificationManager::class.java)
            manager.createNotificationChannel(channel)
        }
    }

    private fun buildNotification(text: String): Notification {
        val openIntent = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )

        val disconnectIntent = PendingIntent.getService(
            this,
            1,
            Intent(this, HsVpnService::class.java).setAction(ACTION_DISCONNECT),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )

        val notifBuilder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, NOTIFICATION_CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }

        return notifBuilder
            .setContentTitle("HS PoC VPN")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.ic_menu_share)
            .setContentIntent(openIntent)
            .addAction(android.R.drawable.ic_menu_close_clear_cancel, "Disconnect", disconnectIntent)
            .setOngoing(true)
            .build()
    }
}