package com.praimate.mobile;

import android.content.Context;
import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;
import org.json.JSONArray;
import org.json.JSONObject;
import java.security.KeyStore;
import java.util.UUID;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/** Profile data is encrypted; the AES key remains in Android Keystore. */
public final class Vault {
    private static final String ALIAS = "praimate.remote.profiles.v1";
    private static final Object LOCK = new Object();
    private final SharedPreferences preferences;

    public Vault(Context context) { preferences = context.getSharedPreferences("remote-vault", Context.MODE_PRIVATE); }

    private SecretKey key() throws Exception {
        KeyStore store = KeyStore.getInstance("AndroidKeyStore"); store.load(null);
        if (store.containsAlias(ALIAS)) return (SecretKey)store.getKey(ALIAS, null);
        KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
        generator.init(new KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).setKeySize(256).build());
        return generator.generateKey();
    }

    private JSONArray read() throws Exception {
        String saved = preferences.getString("profiles", "");
        if (saved.isEmpty()) return new JSONArray();
        JSONObject encrypted = new JSONObject(saved);
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.DECRYPT_MODE, key(), new GCMParameterSpec(128, Base64.decode(encrypted.getString("iv"), Base64.NO_WRAP)));
        byte[] raw = cipher.doFinal(Base64.decode(encrypted.getString("data"), Base64.NO_WRAP));
        return new JSONArray(new String(raw, java.nio.charset.StandardCharsets.UTF_8));
    }

    private void write(JSONArray profiles) throws Exception {
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding"); cipher.init(Cipher.ENCRYPT_MODE, key());
        JSONObject encrypted = new JSONObject().put("iv", Base64.encodeToString(cipher.getIV(), Base64.NO_WRAP))
            .put("data", Base64.encodeToString(cipher.doFinal(profiles.toString().getBytes(java.nio.charset.StandardCharsets.UTF_8)), Base64.NO_WRAP));
        if (!preferences.edit().putString("profiles", encrypted.toString()).commit()) throw new IllegalStateException("Cannot save encrypted device credentials");
    }

    public JSONArray profiles() throws Exception {
        synchronized (LOCK) {
            JSONArray result = new JSONArray(), profiles = read();
            for (int i = 0; i < profiles.length(); i++) { JSONObject p = profiles.getJSONObject(i); result.put(new JSONObject().put("id",p.getString("id")).put("name",p.getString("name")).put("url",p.getString("url")).put("paired",!p.optString("token").isEmpty())); }
            return result;
        }
    }

    public JSONObject profile(String id) throws Exception {
        synchronized (LOCK) { JSONArray profiles = read(); for (int i=0;i<profiles.length();i++) if (profiles.getJSONObject(i).getString("id").equals(id)) return profiles.getJSONObject(i); }
        throw new IllegalArgumentException("Server profile no longer exists");
    }

    public String add(String name, String url) throws Exception {
        url=Endpoint.normalize(url,BuildConfig.DEBUG);
        synchronized (LOCK) {
            JSONArray profiles=read(); if(profiles.length()>=20)throw new IllegalStateException("Remove an unused server before adding another");
            String id=UUID.randomUUID().toString(); profiles.put(new JSONObject().put("id",id).put("name",name.trim().isEmpty()?"PrAImate server":name.trim()).put("url",url).put("token","").put("certificates","")); write(profiles); return id;
        }
    }

    public void set(String id, String field, String value) throws Exception {
        if (!field.equals("token") && !field.equals("certificates") && !field.equals("route")) throw new IllegalArgumentException("Invalid credential field");
        if(field.equals("route"))Endpoint.route(value);
        synchronized (LOCK) { JSONArray profiles=read(); for(int i=0;i<profiles.length();i++) { JSONObject p=profiles.getJSONObject(i); if(p.getString("id").equals(id)){p.put(field,value);if(field.equals("token"))p.put("credentialVersion",p.optLong("credentialVersion")+1);write(profiles);return;} } }
        throw new IllegalArgumentException("Server profile no longer exists");
    }

    public boolean update(String id,String name,String url) throws Exception {
        String normalized=Endpoint.normalize(url,BuildConfig.DEBUG);
        synchronized(LOCK){
            JSONArray profiles=read();
            for(int i=0;i<profiles.length();i++){
                JSONObject profile=profiles.getJSONObject(i);
                if(!profile.getString("id").equals(id))continue;
                boolean changed=!profile.getString("url").equals(normalized);
                profile.put("name",name.trim().isEmpty()?"PrAImate server":name.trim()).put("url",normalized);
                if(changed)profile.put("token","").put("credentialVersion",profile.optLong("credentialVersion")+1).put("certificates","").put("route","").put("accessCookies","").put("accessUserAgent","").put("accessVersion",profile.optLong("accessVersion")+1);
                write(profiles);return changed;
            }
        }
        throw new IllegalArgumentException("Server profile no longer exists");
    }

    public void setCredential(String id,String expectedURL,String token)throws Exception{
        setForServer(id,expectedURL,"token",token);
    }
    public void finishAccess(String id,String expectedURL,long expectedVersion,String cookies,String userAgent)throws Exception{
        String filtered=AccessSession.cookies(cookies);
        if(!filtered.isEmpty()&&(userAgent==null||userAgent.length()>2048||userAgent.indexOf('\r')>=0||userAgent.indexOf('\n')>=0))throw new IllegalArgumentException("Invalid Access client identity");
        synchronized(LOCK){
            JSONArray profiles=read();
            for(int i=0;i<profiles.length();i++){
                JSONObject p=profiles.getJSONObject(i);if(!p.getString("id").equals(id))continue;
                if(!p.getString("url").equals(expectedURL)||p.optLong("accessVersion")!=expectedVersion)throw new IllegalStateException("Cloudflare sign-in was changed or forgotten. Reopen this server.");
                p.put("accessCookies",filtered).put("accessUserAgent",filtered.isEmpty()?"":userAgent).put("accessVersion",expectedVersion+1);write(profiles);return;
            }
        }
        throw new IllegalArgumentException("Server profile no longer exists");
    }
    public void clearAccess(String id)throws Exception{
        synchronized(LOCK){JSONObject p=profile(id);finishAccess(id,p.getString("url"),p.optLong("accessVersion"),"","");}
    }
    public void finishPairing(String id,String expectedURL,long expectedVersion,String token)throws Exception{
        synchronized(LOCK){
            if(profile(id).optLong("credentialVersion")!=expectedVersion)throw new IllegalStateException("Pairing was changed or forgotten. Reopen the server before pairing again.");
            setCredential(id,expectedURL,token);
        }
    }
    public void setForServer(String id,String expectedURL,String field,String value)throws Exception{
        synchronized(LOCK){
            if(!profile(id).getString("url").equals(expectedURL))throw new IllegalStateException("Server address changed. Reopen the server before pairing.");
            set(id,field,value);
        }
    }

    public void remove(String id) throws Exception {
        synchronized (LOCK) { JSONArray profiles=read(), next=new JSONArray(); for(int i=0;i<profiles.length();i++)if(!profiles.getJSONObject(i).getString("id").equals(id))next.put(profiles.getJSONObject(i));write(next); }
    }
}
