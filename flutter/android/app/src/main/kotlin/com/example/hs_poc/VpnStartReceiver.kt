package com.example.hs_poc

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.util.Log

/**
 * Receives broadcasts to trigger VPN start without UI interaction.
 * Works with Device Owner + always-on VPN to skip permission dialog.
 */
class VpnStartReceiver : BroadcastReceiver() {
    companion object {
        const val TAG = "VpnStartReceiver"
        const val ACTION_START = "com.example.hs_poc.ACTION_START_VPN"
    }

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION_START) return

        val privateKey = intent.getStringExtra("privateKey") ?: ""
        val peerKey = intent.getStringExtra("peerKey") ?: ""
        val endpoint = intent.getStringExtra("endpoint") ?: ""
        val localIP = intent.getStringExtra("localIP") ?: "100.64.0.2"
        val peerIP = intent.getStringExtra("peerIP") ?: "100.64.0.1"

        Log.d(TAG, "Received VPN start request")
        Log.d(TAG, "  localIP=$localIP, peerIP=$peerIP, endpoint=$endpoint")

        // With Device Owner + always-on VPN, prepare() should return null
        val prepIntent = VpnService.prepare(context)
        if (prepIntent != null) {
            Log.w(TAG, "VPN permission still required! Dialog would appear.")
            return
        }

        Log.d(TAG, "VPN permission OK, starting service")
        val vpnIntent = Intent(context, HsVpnService::class.java).apply {
            action = HsVpnService.ACTION_CONNECT
            putExtra("privateKey", privateKey)
            putExtra("peerKey", peerKey)
            putExtra("endpoint", endpoint)
            putExtra("localIP", localIP)
            putExtra("peerIP", peerIP)
        }
        context.startService(vpnIntent)
        Log.d(TAG, "HsVpnService start requested")
    }
}